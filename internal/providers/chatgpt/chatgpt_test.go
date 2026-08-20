package chatgpt

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// codexSSE is a minimal Codex-backend stream: one text delta and the terminal
// response.completed envelope.
const codexSSE = "event: response.created\n" +
	`data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"gpt-5.6-terra"}}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","delta":"ok"}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.6-terra","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}}` + "\n\n" +
	"data: [DONE]\n\n"

// tokenWithAccount builds an unsigned JWT carrying the ChatGPT account claim.
func tokenWithAccount(t *testing.T, accountID string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		accountIDClaim: map[string]string{"chatgpt_account_id": accountID},
		"exp":          1787235658,
	})
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(payload) + ".sig"
}

func TestRegistration_TypeIsChatGPT(t *testing.T) {
	if Registration.Type != "chatgpt" {
		t.Errorf("Registration.Type = %q, want %q", Registration.Type, "chatgpt")
	}
	if Registration.New == nil {
		t.Error("Registration.New should not be nil")
	}
	if Registration.Discovery.DefaultBaseURL != defaultBaseURL {
		t.Errorf("DefaultBaseURL = %q, want %q", Registration.Discovery.DefaultBaseURL, defaultBaseURL)
	}
}

// TestStreamResponses_SendsCodexDialect locks the wire contract: the ChatGPT
// Codex backend requires stream/store pinned, rejects public Responses
// parameters it does not implement, and needs a list-shaped input.
func TestStreamResponses_SendsCodexDialect(t *testing.T) {
	var gotPath string
	var gotHeader http.Header
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Clone()
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, codexSSE)
	}))
	defer srv.Close()

	token := tokenWithAccount(t, "acct-123")
	provider := NewWithHTTPClient(token, srv.URL, srv.Client(), llmclient.Hooks{})

	temperature := 0.7
	maxTokens := 128
	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model:              "gpt-5.6-terra",
		Input:              "Reply with exactly ok",
		Instructions:       "You are Codex.",
		Temperature:        &temperature,
		MaxOutputTokens:    &maxTokens,
		PreviousResponseID: "resp_prev",
		Truncation:         "auto",
		User:               "someone",
		Metadata:           map[string]string{"a": "b"},
		Include:            []string{"reasoning.encrypted_content"},
		Reasoning:          &core.Reasoning{Effort: "low"},
	})
	if err != nil {
		t.Fatalf("StreamResponses: %v", err)
	}
	defer func() { _ = stream.Close() }()
	if _, err := io.ReadAll(stream); err != nil {
		t.Fatalf("read stream: %v", err)
	}

	if gotPath != "/responses" {
		t.Errorf("path = %q, want /responses", gotPath)
	}
	if got := gotHeader.Get("Authorization"); got != "Bearer "+token {
		t.Errorf("Authorization header not forwarded")
	}
	if got := gotHeader.Get("chatgpt-account-id"); got != "acct-123" {
		t.Errorf("chatgpt-account-id = %q, want acct-123", got)
	}

	if gotBody["stream"] != true {
		t.Errorf("stream = %v, want true", gotBody["stream"])
	}
	if gotBody["store"] != false {
		t.Errorf("store = %v, want false", gotBody["store"])
	}
	if gotBody["instructions"] != "You are Codex." {
		t.Errorf("instructions = %v", gotBody["instructions"])
	}
	for _, field := range []string{"temperature", "max_output_tokens", "previous_response_id", "truncation", "user", "metadata", "top_p", "service_tier"} {
		if _, ok := gotBody[field]; ok {
			t.Errorf("%s must not be sent to the Codex backend", field)
		}
	}
	input, ok := gotBody["input"].([]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input = %#v, want a one-element list", gotBody["input"])
	}
	msg, _ := input[0].(map[string]any)
	if msg["role"] != "user" || msg["type"] != "message" {
		t.Errorf("input[0] = %#v, want a user message", msg)
	}
	// The Responses API spells input content "input_text"; core.ContentPart
	// would have rewritten it to the Chat Completions "text".
	parts, ok := msg["content"].([]any)
	if !ok || len(parts) != 1 {
		t.Fatalf("content = %#v, want one part", msg["content"])
	}
	part, _ := parts[0].(map[string]any)
	if part["type"] != "input_text" || part["text"] != "Reply with exactly ok" {
		t.Errorf("content[0] = %#v, want an input_text part", part)
	}
}

// TestResponses_CollapsesUpstreamStream covers the non-streaming path: the
// backend refuses stream:false, so GoModel streams and returns the final object.
func TestResponses_CollapsesUpstreamStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, codexSSE)
	}))
	defer srv.Close()

	provider := NewWithHTTPClient("token", srv.URL, srv.Client(), llmclient.Hooks{})
	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "gpt-5.6-terra",
		Input: []core.ResponsesInputElement{{Type: "message", Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Responses: %v", err)
	}
	if resp.Status != "completed" || resp.ID != "resp_1" {
		t.Errorf("resp = %+v, want completed resp_1", resp)
	}
	if len(resp.Output) != 1 || len(resp.Output[0].Content) != 1 || resp.Output[0].Content[0].Text != "ok" {
		t.Errorf("output = %+v, want a single 'ok' text item", resp.Output)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 5 {
		t.Errorf("usage = %+v, want total_tokens 5", resp.Usage)
	}
}

// TestResponses_TruncatedStreamIsAnError guards the non-streaming path against
// serving a stream that stopped early as an empty but successful answer: only a
// terminal lifecycle event may produce a response. Not to be confused with the
// response.incomplete terminal event, which is a legitimate response.
func TestResponses_TruncatedStreamIsAnError(t *testing.T) {
	created := "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"gpt-5.6-terra"}}` + "\n\n"

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "no events at all", body: "data: [DONE]\n\n", want: "ended before completion"},
		{name: "stream cut after response.created", body: created, want: "ended before completion"},
		{
			name: "upstream error event",
			body: created + "event: error\n" +
				`data: {"type":"error","code":"server_error","message":"upstream exploded"}` + "\n\n",
			want: "upstream exploded",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			provider := NewWithHTTPClient("token", srv.URL, srv.Client(), llmclient.Hooks{})
			resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{Model: "gpt-5.6-terra", Input: "hi"})
			if err == nil {
				t.Fatalf("expected an error, got response %+v", resp)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestResponses_NonSuccessTerminalIsReturnedAsAResponse mirrors what the
// Responses API returns for a non-streaming call: a generation that failed or
// stopped short is a response whose status says so, not a transport error.
func TestResponses_NonSuccessTerminalIsReturnedAsAResponse(t *testing.T) {
	tests := []struct {
		name       string
		event      string
		payload    string
		wantStatus string
	}{
		{
			name:       "failed",
			event:      "response.failed",
			payload:    `{"type":"response.failed","response":{"id":"resp_1","object":"response","status":"failed","model":"gpt-5.6-terra","error":{"code":"server_error","message":"boom"}}}`,
			wantStatus: "failed",
		},
		{
			name:       "incomplete",
			event:      "response.incomplete",
			payload:    `{"type":"response.incomplete","response":{"id":"resp_1","object":"response","status":"incomplete","model":"gpt-5.6-terra","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`,
			wantStatus: "incomplete",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: "+tc.event+"\ndata: "+tc.payload+"\n\n")
			}))
			defer srv.Close()

			provider := NewWithHTTPClient("token", srv.URL, srv.Client(), llmclient.Hooks{})
			resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{Model: "gpt-5.6-terra", Input: "hi"})
			if err != nil {
				t.Fatalf("Responses: %v", err)
			}
			if resp.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", resp.Status, tc.wantStatus)
			}
		})
	}
	t.Run("failed carries the upstream error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: response.failed\n"+
				`data: {"type":"response.failed","response":{"id":"resp_1","object":"response","status":"failed","model":"gpt-5.6-terra","error":{"code":"server_error","message":"boom"}}}`+"\n\n")
		}))
		defer srv.Close()

		provider := NewWithHTTPClient("token", srv.URL, srv.Client(), llmclient.Hooks{})
		resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{Model: "gpt-5.6-terra", Input: "hi"})
		if err != nil {
			t.Fatalf("Responses: %v", err)
		}
		if resp.Error == nil || resp.Error.Message != "boom" {
			t.Errorf("resp.Error = %+v, want the upstream error", resp.Error)
		}
	})
}

func TestStreamResponses_RequiresToken(t *testing.T) {
	provider := NewWithHTTPClient("", "http://example.invalid", http.DefaultClient, llmclient.Hooks{})
	_, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{Model: "gpt-5.6-terra", Input: "hi"})
	if err == nil {
		t.Fatal("expected an authentication error without a token")
	}
	if !strings.Contains(err.Error(), "CHATGPT_API_KEY") {
		t.Errorf("error = %q, want it to name CHATGPT_API_KEY", err)
	}
}

func TestListModels(t *testing.T) {
	tests := []struct {
		name       string
		configured []string
		want       []string
	}{
		{name: "defaults", want: defaultModels},
		{name: "configured override", configured: []string{"gpt-5.6-terra"}, want: []string{"gpt-5.6-terra"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := New(providers.ProviderConfig{APIKey: "token"}, providers.ProviderOptions{Models: tc.configured})
			resp, err := provider.ListModels(context.Background())
			if err != nil {
				t.Fatalf("ListModels: %v", err)
			}
			if len(resp.Data) != len(tc.want) {
				t.Fatalf("got %d models, want %d", len(resp.Data), len(tc.want))
			}
			for i, model := range resp.Data {
				if model.ID != tc.want[i] {
					t.Errorf("model[%d] = %q, want %q", i, model.ID, tc.want[i])
				}
			}
		})
	}
}

// TestUnsupportedSurfaces checks that surfaces the Codex backend does not
// implement report a capability gap (501) rather than a malformed request.
func TestUnsupportedSurfaces(t *testing.T) {
	provider := New(providers.ProviderConfig{APIKey: "token"}, providers.ProviderOptions{})
	calls := map[string]func() error{
		"ChatCompletion": func() error {
			_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gpt-5.6-terra"})
			return err
		},
		"StreamChatCompletion": func() error {
			_, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{Model: "gpt-5.6-terra"})
			return err
		},
		"Embeddings": func() error {
			_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{Model: "gpt-5.6-terra"})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatal("expected an unsupported-surface error")
			}
			var gatewayErr *core.GatewayError
			if !errors.As(err, &gatewayErr) {
				t.Fatalf("error = %T, want *core.GatewayError", err)
			}
			if gatewayErr.StatusCode != http.StatusNotImplemented {
				t.Errorf("status = %d, want %d", gatewayErr.StatusCode, http.StatusNotImplemented)
			}
			// The code is the programmatic half of the contract: callers
			// branch on it to tell a capability gap from a bad request.
			if gatewayErr.Code == nil || *gatewayErr.Code != unsupportedOperationCode {
				t.Errorf("code = %v, want %q", gatewayErr.Code, unsupportedOperationCode)
			}
		})
	}
}

func TestAccountIDFromToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{name: "chatgpt token", token: tokenWithAccount(t, "acct-9"), want: "acct-9"},
		{name: "not a jwt", token: "sk-plain-key", want: ""},
		{name: "jwt without claim", token: "e30.e30.sig", want: ""},
		{name: "undecodable payload", token: "e30.!!!.sig", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := accountIDFromToken(tc.token); got != tc.want {
				t.Errorf("accountIDFromToken() = %q, want %q", got, tc.want)
			}
		})
	}
}
