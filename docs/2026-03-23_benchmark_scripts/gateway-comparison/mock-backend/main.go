// Mock OpenAI-compatible backend server for benchmarking AI gateways.
// Responds instantly with deterministic payloads so benchmarks measure
// pure gateway overhead, not provider latency.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	port := "9999"
	if p := os.Getenv("MOCK_PORT"); p != "" {
		port = p
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", handleChatCompletions)
	mux.HandleFunc("/chat/completions", handleChatCompletions) // some gateways strip /v1
	mux.HandleFunc("/v1/responses", handleResponses)
	mux.HandleFunc("/responses", handleResponses)
	mux.HandleFunc("/v1/models", handleModels)
	mux.HandleFunc("/models", handleModels)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBytes(w, http.StatusOK, []byte(`{"status":"ok"}`))
	})

	log.Printf("Mock OpenAI backend listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatal(err)
	}
}

// ---------- Chat Completions ----------

func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Stream bool `json:"stream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if body.Stream {
		streamChatCompletion(w)
	} else {
		nonStreamChatCompletion(w)
	}
}

func nonStreamChatCompletion(w http.ResponseWriter) {
	now := time.Now().Unix()
	resp := map[string]any{
		"id":      "chatcmpl-bench-001",
		"object":  "chat.completion",
		"created": now,
		"model":   "gpt-4o-mini",
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "This is a benchmark response from the mock backend server. It contains enough text to be representative of a typical short AI response that would be returned in production use cases.",
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     25,
			"completion_tokens": 35,
			"total_tokens":      60,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("encode chat response: %v", err)
	}
}

var streamChunks = []string{
	"This ", "is ", "a ", "benchmark ", "response ", "from ", "the ", "mock ",
	"backend ", "server. ", "It ", "contains ", "enough ", "text ", "to ", "be ",
	"representative ", "of ", "a ", "typical ", "short ", "AI ", "response ",
	"that ", "would ", "be ", "returned ", "in ", "production ", "use ", "cases.",
}

func streamChatCompletion(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", 500)
		return
	}

	now := time.Now().Unix()

	// First chunk with role
	chunk := fmt.Sprintf(`{"id":"chatcmpl-bench-001","object":"chat.completion.chunk","created":%d,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`, now)
	fmt.Fprintf(w, "data: %s\n\n", chunk)
	flusher.Flush()

	// Content chunks
	for _, token := range streamChunks {
		chunk = fmt.Sprintf(`{"id":"chatcmpl-bench-001","object":"chat.completion.chunk","created":%d,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"%s"},"finish_reason":null}]}`, now, token)
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		flusher.Flush()
	}

	// Final chunk
	chunk = fmt.Sprintf(`{"id":"chatcmpl-bench-001","object":"chat.completion.chunk","created":%d,"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":25,"completion_tokens":35,"total_tokens":60}}`, now)
	fmt.Fprintf(w, "data: %s\n\n", chunk)
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// ---------- Responses API ----------

func handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Stream bool `json:"stream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if body.Stream {
		streamResponses(w)
	} else {
		nonStreamResponses(w)
	}
}

func nonStreamResponses(w http.ResponseWriter) {
	now := time.Now().Unix()
	resp := map[string]any{
		"id":         "resp-bench-001",
		"object":     "response",
		"created_at": now,
		"model":      "gpt-4o-mini",
		"status":     "completed",
		"output": []map[string]any{
			{
				"type": "message",
				"id":   "msg-bench-001",
				"role": "assistant",
				"content": []map[string]any{
					{
						"type": "output_text",
						"text": "This is a benchmark response from the mock backend server. It contains enough text to be representative of a typical short AI response.",
					},
				},
			},
		},
		"usage": map[string]any{
			"input_tokens":  25,
			"output_tokens": 35,
			"total_tokens":  60,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("encode responses response: %v", err)
	}
}

func streamResponses(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", 500)
		return
	}

	now := time.Now().Unix()
	fullText := strings.Join(streamChunks, "")

	// response.created
	fmt.Fprintf(w, "event: response.created\ndata: %s\n\n",
		mustJSON(map[string]any{"id": "resp-bench-001", "object": "response", "created_at": now, "model": "gpt-4o-mini", "status": "in_progress", "output": []any{}}))
	flusher.Flush()

	// response.output_item.added
	fmt.Fprintf(w, "event: response.output_item.added\ndata: %s\n\n",
		mustJSON(map[string]any{"type": "message", "id": "msg-bench-001", "role": "assistant", "content": []any{}}))
	flusher.Flush()

	// response.content_part.added
	fmt.Fprintf(w, "event: response.content_part.added\ndata: %s\n\n",
		mustJSON(map[string]any{"type": "output_text", "text": ""}))
	flusher.Flush()

	// text deltas
	for _, token := range streamChunks {
		fmt.Fprintf(w, "event: response.output_text.delta\ndata: %s\n\n",
			mustJSON(map[string]any{"type": "response.output_text.delta", "delta": token}))
		flusher.Flush()
	}

	// response.output_text.done
	fmt.Fprintf(w, "event: response.output_text.done\ndata: %s\n\n",
		mustJSON(map[string]any{"type": "response.output_text.done", "text": fullText}))
	flusher.Flush()

	// response.completed
	fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n",
		mustJSON(map[string]any{
			"id": "resp-bench-001", "object": "response", "status": "completed",
			"output": []map[string]any{{"type": "message", "id": "msg-bench-001", "role": "assistant",
				"content": []map[string]any{{"type": "output_text", "text": fullText}}}},
			"usage": map[string]any{"input_tokens": 25, "output_tokens": 35, "total_tokens": 60},
		}))
	flusher.Flush()
}

// ---------- Models ----------

func handleModels(w http.ResponseWriter, _ *http.Request) {
	resp := map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"id": "gpt-4o-mini", "object": "model", "owned_by": "openai", "created": time.Now().Unix()},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("encode models response: %v", err)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func writeJSONBytes(w http.ResponseWriter, status int, payload []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(payload); err != nil {
		log.Printf("write response: %v", err)
	}
}
