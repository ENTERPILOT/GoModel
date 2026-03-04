//go:build contract

package contract

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type replayRoute struct {
	statusCode  int
	contentType string
	body        []byte
}

type replayTransport struct {
	t      *testing.T
	routes map[string]replayRoute
}

func replayKey(method, requestURI string) string {
	return method + " " + requestURI
}

func (rt *replayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.t.Helper()

	key := replayKey(req.Method, req.URL.RequestURI())
	route, ok := rt.routes[key]
	if !ok {
		notFoundBody := []byte(fmt.Sprintf(`{"error":{"message":"missing replay route: %s"}}`, key))
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Status:     "404 Not Found",
			Header: http.Header{
				"Content-Type": []string{"application/json"},
			},
			Body:    io.NopCloser(bytes.NewReader(notFoundBody)),
			Request: req,
		}, nil
	}

	statusCode := route.statusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	contentType := route.contentType
	if contentType == "" {
		contentType = "application/json"
	}

	return &http.Response{
		StatusCode: statusCode,
		Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		Header: http.Header{
			"Content-Type": []string{contentType},
		},
		Body:    io.NopCloser(bytes.NewReader(route.body)),
		Request: req,
	}, nil
}

func newReplayHTTPClient(t *testing.T, routes map[string]replayRoute) *http.Client {
	t.Helper()
	return &http.Client{
		Transport: &replayTransport{
			t:      t,
			routes: routes,
		},
	}
}

func jsonFixtureRoute(t *testing.T, path string) replayRoute {
	t.Helper()
	return replayRoute{
		statusCode:  http.StatusOK,
		contentType: "application/json",
		body:        loadGoldenFileRaw(t, path),
	}
}

func sseFixtureRoute(t *testing.T, path string) replayRoute {
	t.Helper()
	return replayRoute{
		statusCode:  http.StatusOK,
		contentType: "text/event-stream",
		body:        loadGoldenFileRaw(t, path),
	}
}

type sseEvent struct {
	Name string
	Data string
}

func parseSSEEvents(t *testing.T, raw []byte) []sseEvent {
	t.Helper()

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	// Stream fixtures can contain long payload lines.
	scanner.Buffer(make([]byte, 1024), 1024*1024)

	events := make([]sseEvent, 0)
	currentName := ""
	dataLines := make([]string, 0, 1)

	flush := func() {
		if len(dataLines) == 0 {
			currentName = ""
			return
		}
		events = append(events, sseEvent{
			Name: currentName,
			Data: strings.Join(dataLines, "\n"),
		})
		currentName = ""
		dataLines = dataLines[:0]
	}

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			currentName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimPrefix(line, "data:")
			// Per SSE format, a single optional space may follow the colon.
			data = strings.TrimPrefix(data, " ")
			dataLines = append(dataLines, data)
		}
	}
	require.NoError(t, scanner.Err())
	flush()

	return events
}

func readAllStream(t *testing.T, body io.ReadCloser) []byte {
	t.Helper()
	defer func() { _ = body.Close() }()

	data, err := io.ReadAll(body)
	require.NoError(t, err)
	return data
}

func parseChatStream(t *testing.T, raw []byte) ([]map[string]any, bool) {
	t.Helper()

	events := parseSSEEvents(t, raw)
	chunks := make([]map[string]any, 0, len(events))
	done := false

	for _, e := range events {
		if e.Data == "[DONE]" {
			done = true
			continue
		}
		var chunk map[string]any
		err := json.Unmarshal([]byte(e.Data), &chunk)
		require.NoError(t, err)
		chunks = append(chunks, chunk)
	}

	return chunks, done
}

func extractChatStreamText(chunks []map[string]any) string {
	var b strings.Builder
	for _, chunk := range chunks {
		choices, ok := chunk["choices"].([]any)
		if !ok || len(choices) == 0 {
			continue
		}
		choice, ok := choices[0].(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}
		text, ok := delta["content"].(string)
		if ok {
			b.WriteString(text)
		}
	}
	return b.String()
}

type responsesStreamEvent struct {
	Name    string
	Payload map[string]any
	Done    bool
}

func parseResponsesStream(t *testing.T, raw []byte) []responsesStreamEvent {
	t.Helper()

	events := parseSSEEvents(t, raw)
	out := make([]responsesStreamEvent, 0, len(events))

	for _, e := range events {
		if e.Data == "[DONE]" {
			out = append(out, responsesStreamEvent{Done: true})
			continue
		}

		payload := make(map[string]any)
		err := json.Unmarshal([]byte(e.Data), &payload)
		require.NoError(t, err)

		name := e.Name
		if name == "" {
			if typ, ok := payload["type"].(string); ok {
				name = typ
			}
		}

		out = append(out, responsesStreamEvent{
			Name:    name,
			Payload: payload,
		})
	}

	return out
}

func hasResponsesEvent(events []responsesStreamEvent, name string) bool {
	for _, e := range events {
		if e.Name == name {
			return true
		}
	}
	return false
}

func extractResponsesStreamText(events []responsesStreamEvent) string {
	var b strings.Builder
	for _, e := range events {
		if e.Name != "response.output_text.delta" {
			continue
		}
		delta, ok := e.Payload["delta"].(string)
		if ok {
			b.WriteString(delta)
		}
	}
	return b.String()
}
