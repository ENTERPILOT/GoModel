// Command mockjev serves deterministic TypeSafe System One upstreams for the
// release E2E curl matrix, one per path prefix, so a single process backs
// several jev providers:
//
//	/jev   hosted-API shape: requires "Authorization: Bearer $MOCK_JEV_KEY";
//	       lists jev-latest and jev-preview by "name"; accepts any versioned
//	       ID (jev-1.13.0); answers jev-latest as jev-1.13.0; has no Kev
//	       diagnostic routes (404, like the hosted API).
//	/kev   Kev-server shape: no authentication; lists checkpoint kev-latest
//	       by "id" with alias kev-4b; serves /v1/systemone/permute and
//	       /v1/systemone/separate.
//	/down  lists kev-down but answers every System One route with 529, the
//	       status TypeSafe uses for overload, so failover can be exercised.
//
// Each answer carries a "mock" object echoing what reached the upstream (the
// model, state, questions, extra top-level fields, whether an Authorization
// header arrived, the X-Request-Id, and a per-upstream request sequence), so
// scenarios can assert what the gateway forwarded and whether a cached
// answer was replayed. Malformed questions get a FastAPI-style 422, as the
// hosted API returns.
//
// PORT selects the listen port (default 18091). GET /healthz reports liveness.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

type upstream struct {
	name     string
	key      string // required bearer key; empty means no authentication
	models   any    // GET /v1/models body
	kevRoute bool   // serves permute and separate
	down     bool   // answers System One routes with 529
	accepts  func(model string) (answeredAs string, ok bool)

	mu  sync.Mutex
	seq int
}

var versionedJev = regexp.MustCompile(`^jev-\d+\.\d+\.\d+$`)

func newUpstreams(jevKey string) []*upstream {
	return []*upstream{
		{
			name: "jev",
			key:  jevKey,
			models: map[string]any{"models": []map[string]any{
				{"name": "jev-latest", "description": "Latest Jev (mock)", "release_date": "2026-06-01"},
				{"name": "jev-preview", "description": "Preview Jev (mock)"},
			}},
			accepts: func(model string) (string, bool) {
				switch {
				case model == "jev-latest":
					return "jev-1.13.0", true
				case model == "jev-preview":
					return "jev-1.14.0-preview", true
				case versionedJev.MatchString(model):
					return model, true
				}
				return "", false
			},
		},
		{
			name:     "kev",
			kevRoute: true,
			models: map[string]any{"models": []map[string]any{
				{"id": "kev-latest", "aliases": []string{"kev-4b"}, "release_date": "2026-09-01T00:00:00Z"},
			}},
			accepts: func(model string) (string, bool) {
				if model == "kev-latest" || model == "kev-4b" {
					return "kev-4b-e2e", true
				}
				return "", false
			},
		},
		{
			name:     "down",
			kevRoute: true,
			down:     true,
			models:   map[string]any{"models": []map[string]any{{"id": "kev-down"}}},
			accepts:  func(string) (string, bool) { return "", false },
		},
	}
}

type question struct {
	Type         string          `json:"type"`
	Instructions string          `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria"`
}

type request struct {
	Model     string              `json:"model"`
	State     json.RawMessage     `json:"state"`
	Questions map[string]question `json:"questions"`
	NPerm     *int                `json:"n_perm"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// validationError mirrors the FastAPI 422 body the hosted API returns.
func validationError(w http.ResponseWriter, loc []any, msg string) {
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
		"detail": []map[string]any{{"loc": append([]any{"body"}, loc...), "msg": msg, "type": "value_error"}},
	})
}

func (u *upstream) authorized(r *http.Request) bool {
	return u.key == "" || r.Header.Get("Authorization") == "Bearer "+u.key
}

func (u *upstream) authSeen(r *http.Request) string {
	switch auth := r.Header.Get("Authorization"); {
	case auth == "":
		return "none"
	case u.key != "" && auth == "Bearer "+u.key:
		return "provider-key"
	default:
		return "other"
	}
}

func (u *upstream) serveModels(w http.ResponseWriter, r *http.Request) {
	if !u.authorized(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "Invalid API key"})
		return
	}
	writeJSON(w, http.StatusOK, u.models)
}

func (u *upstream) serveSystemOne(route string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"detail": "Method Not Allowed"})
			return
		}
		if route != "evaluate" && !u.kevRoute {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Not Found"})
			return
		}
		if !u.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"detail": "Invalid API key"})
			return
		}
		if u.down {
			writeJSON(w, 529, map[string]any{"detail": "Overloaded (mock " + u.name + ")"})
			return
		}
		var raw bytes.Buffer
		if _, err := raw.ReadFrom(r.Body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
			return
		}
		var req request
		if err := json.Unmarshal(raw.Bytes(), &req); err != nil {
			validationError(w, nil, "invalid JSON: "+err.Error())
			return
		}
		answeredAs, ok := u.accepts(req.Model)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"detail": fmt.Sprintf("Model %q not found", req.Model)})
			return
		}
		if len(req.State) == 0 || string(req.State) == "null" {
			validationError(w, []any{"state"}, "Field required")
			return
		}
		if len(req.Questions) == 0 {
			validationError(w, []any{"questions"}, "At least one question is required")
			return
		}
		names := make([]string, 0, len(req.Questions))
		for name := range req.Questions {
			names = append(names, name)
		}
		sort.Strings(names)

		nPerm := 0
		if route == "permute" {
			if len(req.Questions) != 1 || req.Questions[names[0]].Type != "choice" {
				validationError(w, []any{"questions"}, "permute takes exactly one choice question")
				return
			}
			nPerm = 6
			if req.NPerm != nil {
				nPerm = *req.NPerm
			}
			if nPerm < 1 || nPerm > 64 {
				validationError(w, []any{"n_perm"}, "n_perm must be between 1 and 64")
				return
			}
		}

		answers := make(map[string]any, len(names))
		for _, name := range names {
			answer, msg := answerFor(req.Questions[name])
			if msg != "" {
				validationError(w, []any{"questions", name}, msg)
				return
			}
			answers[name] = answer
		}

		u.mu.Lock()
		u.seq++
		seq := u.seq
		u.mu.Unlock()

		var top map[string]json.RawMessage
		_ = json.Unmarshal(raw.Bytes(), &top)
		extra := map[string]json.RawMessage{}
		for key, value := range top {
			switch key {
			case "model", "state", "questions", "n_perm":
			default:
				extra[key] = value
			}
		}

		body := map[string]any{
			"model":   answeredAs,
			"answers": answers,
			"usage": map[string]any{
				"input_tokens":  10 + len(req.State)/4 + 5*len(names),
				"output_tokens": 3 * len(names),
			},
			"mock": map[string]any{
				"upstream":        u.name,
				"route":           route,
				"received_model":  req.Model,
				"received_state":  req.State,
				"questions":       top["questions"],
				"extra":           extra,
				"authorization":   u.authSeen(r),
				"request_id":      r.Header.Get("X-Request-Id"),
				"request_seq":     seq,
				"received_length": raw.Len(),
			},
		}
		if route == "permute" {
			body["n_perm"] = nPerm
		}
		if route == "separate" {
			body["separate"] = true
		}
		writeJSON(w, http.StatusOK, body)
	}
}

// answerFor builds a deterministic, well-formed answer for one question, or
// returns the validation message the hosted API would reject it with.
func answerFor(q question) (any, string) {
	switch q.Type {
	case "noul":
		return map[string]any{"type": "noul", "noul": 0.93}, ""
	case "choice":
		var criteria map[string]string
		if err := json.Unmarshal(q.Criteria, &criteria); err != nil || len(criteria) < 2 {
			return nil, "choice criteria must map at least two option names to descriptions"
		}
		options := make([]string, 0, len(criteria))
		for option := range criteria {
			options = append(options, option)
		}
		sort.Strings(options)
		probabilities := make(map[string]float64, len(options))
		rest := 0.4 / float64(len(options)-1)
		for i, option := range options {
			probabilities[option] = rest
			if i == 0 {
				probabilities[option] = 0.6
			}
		}
		return map[string]any{"type": "choice", "choice": options[0], "confidence": 0.5, "probabilities": probabilities}, ""
	case "score":
		var levels []string
		if err := json.Unmarshal(q.Criteria, &levels); err != nil || len(levels) < 2 {
			return nil, "score criteria must list at least two ordered levels"
		}
		legend := make(map[string]string, len(levels))
		probabilities := make(map[string]float64, len(levels))
		for i, level := range levels {
			legend[fmt.Sprint(i)] = level
			probabilities[fmt.Sprint(i)] = 0
		}
		probabilities["1"] = 1
		return map[string]any{"type": "score", "score": 1.0, "confidence": 0.8, "legend": legend, "probabilities": probabilities}, ""
	case "":
		return nil, "Field required: type"
	default:
		return nil, fmt.Sprintf("Input tag %q found using 'type' does not match any of the expected tags: 'noul', 'choice', 'score'", q.Type)
	}
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "18091"
	}
	jevKey := os.Getenv("MOCK_JEV_KEY")
	if jevKey == "" {
		jevKey = "qa-mock-jev-key"
	}

	mux := http.NewServeMux()
	for _, u := range newUpstreams(jevKey) {
		prefix := "/" + u.name
		mux.HandleFunc(prefix+"/v1/models", u.serveModels)
		mux.HandleFunc(prefix+"/v1/systemone", u.serveSystemOne("evaluate"))
		mux.HandleFunc(prefix+"/v1/systemone/permute", u.serveSystemOne("permute"))
		mux.HandleFunc(prefix+"/v1/systemone/separate", u.serveSystemOne("separate"))
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "Not Found: " + strings.TrimSpace(r.URL.Path)})
	})

	log.Printf("mockjev listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
