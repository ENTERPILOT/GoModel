// Mock OpenAI/Anthropic-compatible backend for gateway benchmarking.
//
// It answers instantly with deterministic payloads so the benchmark measures
// pure gateway overhead, not upstream model latency. Three dialects are served
// so every gateway can be exercised through its own native translation path:
//
//	/v1/chat/completions  — OpenAI Chat Completions (stream + non-stream)
//	/v1/responses         — OpenAI Responses        (stream + non-stream)
//	/v1/messages          — Anthropic Messages      (stream + non-stream)
//
// Each path is also exposed without the /v1 prefix because some gateways strip
// it before forwarding upstream.
//
// Recording mode (MOCK_RECORD=1): every upstream request is captured (method,
// path, headers with secrets redacted, body) along with the canned response the
// mock returned, and exposed via control endpoints so a harness can inspect how
// each gateway *translated* a client request:
//
//	POST /__reset   clear the capture log
//	GET  /__log     {"entries":[...]} all captured exchanges since reset
//	GET  /__last    the most recent captured exchange
//
// Recording also enriches responses with provider-specific extras
// (system_fingerprint, service_tier, x_provider_note) so response-normalization
// fidelity is observable. Both behaviors are gated so the latency benchmark
// stays byte-identical when MOCK_RECORD is unset.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

func main() {
	port := "9999"
	if p := os.Getenv("MOCK_PORT"); p != "" {
		port = p
	}

	mux := http.NewServeMux()
	register(mux, "/chat/completions", handleChatCompletions)
	register(mux, "/responses", handleResponses)
	register(mux, "/messages", handleMessages)
	mux.HandleFunc("/v1/models", handleModels)
	mux.HandleFunc("/models", handleModels)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBytes(w, http.StatusOK, []byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/__reset", handleReset)
	mux.HandleFunc("/__log", handleLog)
	mux.HandleFunc("/__last", handleLast)

	log.Printf("Mock backend (openai+anthropic) listening on :%s (record=%v)", port, recording())
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}

// register binds a handler at both the canonical and /v1-prefixed path.
func register(mux *http.ServeMux, path string, h http.HandlerFunc) {
	mux.HandleFunc(path, h)
	mux.HandleFunc("/v1"+path, h)
}

// ---------- request/response capture ----------

func recording() bool { return os.Getenv("MOCK_RECORD") == "1" }

// entry is one captured upstream exchange: the request a gateway sent and the
// response the mock returned for it.
type entry struct {
	Seq      int               `json:"seq"`
	Time     string            `json:"time"`
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	Query    string            `json:"query,omitempty"`
	Headers  map[string]string `json:"headers"`
	Body     json.RawMessage   `json:"body,omitempty"`
	BodyText string            `json:"body_text,omitempty"` // set when body is not valid JSON
	Stream   bool              `json:"stream"`
	Response any               `json:"response,omitempty"`
}

var rec struct {
	mu      sync.Mutex
	entries []*entry
	seq     int
}

var sensitiveHeaders = map[string]bool{
	"authorization": true, "x-api-key": true, "api-key": true,
	"x-portkey-api-key": true, "x-goog-api-key": true,
}

// begin reads and (in recording mode) captures the request, returning the entry
// so the handler can attach the response it produces. Returns ok=false if the
// request was already rejected.
func begin(w http.ResponseWriter, r *http.Request) (*entry, bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil, false
	}
	raw, _ := io.ReadAll(r.Body)
	var sr streamReq
	if err := json.Unmarshal(raw, &sr); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return nil, false
	}
	e := &entry{
		Time: time.Now().UTC().Format(time.RFC3339Nano), Method: r.Method,
		Path: r.URL.Path, Query: r.URL.RawQuery, Headers: captureHeaders(r),
		Stream: sr.Stream,
	}
	if json.Valid(raw) {
		e.Body = json.RawMessage(raw)
	} else {
		e.BodyText = string(raw)
	}
	if recording() {
		rec.mu.Lock()
		rec.seq++
		e.Seq = rec.seq
		rec.entries = append(rec.entries, e)
		rec.mu.Unlock()
	}
	return e, true
}

func captureHeaders(r *http.Request) map[string]string {
	h := make(map[string]string, len(r.Header))
	for k, v := range r.Header {
		val := strings.Join(v, ", ")
		if sensitiveHeaders[strings.ToLower(k)] {
			val = fmt.Sprintf("redacted(len=%d)", len(val))
		}
		h[k] = val
	}
	return h
}

func handleReset(w http.ResponseWriter, _ *http.Request) {
	rec.mu.Lock()
	rec.entries = nil
	rec.seq = 0
	rec.mu.Unlock()
	writeJSONBytes(w, http.StatusOK, []byte(`{"ok":true}`))
}

func handleLog(w http.ResponseWriter, _ *http.Request) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	writeJSON(w, map[string]any{"entries": rec.entries})
}

func handleLast(w http.ResponseWriter, _ *http.Request) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.entries) == 0 {
		writeJSONBytes(w, http.StatusNotFound, []byte(`{"error":"no entries"}`))
		return
	}
	writeJSON(w, rec.entries[len(rec.entries)-1])
}

// streamTokens is the deterministic body streamed token-by-token. Kept short and
// fixed so every run transfers identical bytes.
var streamTokens = []string{
	"This ", "is ", "a ", "benchmark ", "response ", "from ", "the ", "mock ",
	"backend ", "server. ", "It ", "contains ", "enough ", "text ", "to ", "be ",
	"representative ", "of ", "a ", "typical ", "short ", "AI ", "response ",
	"that ", "would ", "be ", "returned ", "in ", "production ", "use ", "cases.",
}

func fullText() string { return strings.Join(streamTokens, "") }

// providerExtras returns provider-specific fields (only in recording mode) so
// response-normalization fidelity is observable across gateways.
func providerExtras() map[string]any {
	if !recording() {
		return nil
	}
	return map[string]any{
		"system_fingerprint": "fp_mock_0001",
		"service_tier":       "default",
		"x_provider_note":    "mock-extra-field",
	}
}

func merge(base map[string]any, extra map[string]any) map[string]any {
	for k, v := range extra {
		base[k] = v
	}
	return base
}

// ---------- OpenAI Chat Completions ----------

func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	e, ok := begin(w, r)
	if !ok {
		return
	}
	if e.Stream {
		streamChatCompletion(w, e)
	} else {
		nonStreamChatCompletion(w, e)
	}
}

func nonStreamChatCompletion(w http.ResponseWriter, e *entry) {
	resp := merge(map[string]any{
		"id":      "chatcmpl-bench-001",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "gpt-4o-mini",
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": fullText()},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 25, "completion_tokens": 35, "total_tokens": 60},
	}, providerExtras())
	respond(w, e, resp)
}

func streamChatCompletion(w http.ResponseWriter, e *entry) {
	flusher := beginSSE(w)
	if flusher == nil {
		return
	}
	setStreamResp(e, "chat.completion.chunk")
	now := time.Now().Unix()
	send(w, flusher, "", fmt.Sprintf(`{"id":"chatcmpl-bench-001","object":"chat.completion.chunk","created":%d,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`, now))
	for _, tok := range streamTokens {
		send(w, flusher, "", fmt.Sprintf(`{"id":"chatcmpl-bench-001","object":"chat.completion.chunk","created":%d,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":%q},"finish_reason":null}]}`, now, tok))
	}
	send(w, flusher, "", fmt.Sprintf(`{"id":"chatcmpl-bench-001","object":"chat.completion.chunk","created":%d,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":35,"total_tokens":60}}`, now))
	send(w, flusher, "", "[DONE]")
}

// ---------- OpenAI Responses ----------

func handleResponses(w http.ResponseWriter, r *http.Request) {
	e, ok := begin(w, r)
	if !ok {
		return
	}
	if e.Stream {
		streamResponses(w, e)
	} else {
		nonStreamResponses(w, e)
	}
}

func nonStreamResponses(w http.ResponseWriter, e *entry) {
	resp := merge(map[string]any{
		"id": "resp-bench-001", "object": "response", "created_at": time.Now().Unix(),
		"model": "gpt-4o-mini", "status": "completed",
		"output": []map[string]any{{
			"type": "message", "id": "msg-bench-001", "role": "assistant",
			"content": []map[string]any{{"type": "output_text", "text": fullText()}},
		}},
		"usage": map[string]any{"input_tokens": 25, "output_tokens": 35, "total_tokens": 60},
	}, providerExtras())
	respond(w, e, resp)
}

func streamResponses(w http.ResponseWriter, e *entry) {
	flusher := beginSSE(w)
	if flusher == nil {
		return
	}
	setStreamResp(e, "response.*")
	now := time.Now().Unix()
	send(w, flusher, "response.created", mustJSON(map[string]any{"id": "resp-bench-001", "object": "response", "created_at": now, "model": "gpt-4o-mini", "status": "in_progress", "output": []any{}}))
	send(w, flusher, "response.output_item.added", mustJSON(map[string]any{"type": "message", "id": "msg-bench-001", "role": "assistant", "content": []any{}}))
	send(w, flusher, "response.content_part.added", mustJSON(map[string]any{"type": "output_text", "text": ""}))
	for _, tok := range streamTokens {
		send(w, flusher, "response.output_text.delta", mustJSON(map[string]any{"type": "response.output_text.delta", "delta": tok}))
	}
	send(w, flusher, "response.output_text.done", mustJSON(map[string]any{"type": "response.output_text.done", "text": fullText()}))
	send(w, flusher, "response.completed", mustJSON(map[string]any{
		"id": "resp-bench-001", "object": "response", "status": "completed",
		"output": []map[string]any{{"type": "message", "id": "msg-bench-001", "role": "assistant",
			"content": []map[string]any{{"type": "output_text", "text": fullText()}}}},
		"usage": map[string]any{"input_tokens": 25, "output_tokens": 35, "total_tokens": 60},
	}))
}

// ---------- Anthropic Messages ----------

func handleMessages(w http.ResponseWriter, r *http.Request) {
	e, ok := begin(w, r)
	if !ok {
		return
	}
	if e.Stream {
		streamMessages(w, e)
	} else {
		nonStreamMessages(w, e)
	}
}

func nonStreamMessages(w http.ResponseWriter, e *entry) {
	resp := merge(map[string]any{
		"id": "msg-bench-001", "type": "message", "role": "assistant",
		"model":   "claude-3-5-sonnet",
		"content": []map[string]any{{"type": "text", "text": fullText()}},
		"stop_reason": "end_turn", "stop_sequence": nil,
		"usage": map[string]any{"input_tokens": 25, "output_tokens": 35},
	}, providerExtras())
	respond(w, e, resp)
}

func streamMessages(w http.ResponseWriter, e *entry) {
	flusher := beginSSE(w)
	if flusher == nil {
		return
	}
	setStreamResp(e, "message_*")
	send(w, flusher, "message_start", mustJSON(map[string]any{"type": "message_start", "message": map[string]any{
		"id": "msg-bench-001", "type": "message", "role": "assistant", "model": "claude-3-5-sonnet",
		"content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": 25, "output_tokens": 1},
	}}))
	send(w, flusher, "content_block_start", mustJSON(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}}))
	for _, tok := range streamTokens {
		send(w, flusher, "content_block_delta", mustJSON(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": tok}}))
	}
	send(w, flusher, "content_block_stop", mustJSON(map[string]any{"type": "content_block_stop", "index": 0}))
	send(w, flusher, "message_delta", mustJSON(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 35}}))
	send(w, flusher, "message_stop", mustJSON(map[string]any{"type": "message_stop"}))
}

// ---------- Models ----------

func handleModels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"id": "gpt-4o-mini", "object": "model", "owned_by": "openai", "created": time.Now().Unix()},
			{"id": "claude-3-5-sonnet", "object": "model", "owned_by": "anthropic", "created": time.Now().Unix()},
		},
	})
}

// ---------- Shared helpers ----------

// streamReq is the only field the mock needs to decode from any request body.
type streamReq struct {
	Stream bool `json:"stream"`
}

// respond writes a non-stream JSON response and records it on the entry.
func respond(w http.ResponseWriter, e *entry, v map[string]any) {
	e.Response = v
	writeJSON(w, v)
}

// setStreamResp records a compact description of a streamed canned response.
func setStreamResp(e *entry, kind string) {
	e.Response = map[string]any{"stream": true, "event_kind": kind, "text": fullText()}
}

func beginSSE(w http.ResponseWriter) http.Flusher {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return nil
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	return flusher
}

// send writes one SSE frame. An empty event name omits the event: line (OpenAI
// chat style); a name emits "event: <name>" (Responses / Anthropic style).
func send(w http.ResponseWriter, flusher http.Flusher, event, data string) {
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode response: %v", err)
	}
}

func writeJSONBytes(w http.ResponseWriter, status int, payload []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(payload); err != nil {
		log.Printf("write response: %v", err)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
