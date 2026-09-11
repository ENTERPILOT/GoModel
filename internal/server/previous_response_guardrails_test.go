package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/responsestore"
)

// redactingPatcher stands in for a prompt guardrail such as presidio: it
// rewrites a word anywhere in a Responses input and records each input it
// saw.
type redactingPatcher struct{ seen []string }

func (p *redactingPatcher) PatchChatRequest(_ context.Context, req *core.ChatRequest) (*core.ChatRequest, error) {
	return req, nil
}

func (p *redactingPatcher) PatchResponsesRequest(_ context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	raw, err := json.Marshal(req.Input)
	if err != nil {
		return nil, err
	}
	p.seen = append(p.seen, string(raw))
	var input any
	if err := json.Unmarshal([]byte(strings.ReplaceAll(string(raw), "zebra", "[animal]")), &input); err != nil {
		return nil, err
	}
	patched := *req
	patched.Input = input
	return &patched, nil
}

func forwardedInput(t *testing.T, provider *capturingProvider) string {
	t.Helper()
	if provider.capturedResponsesReq == nil {
		t.Fatal("provider did not receive a responses request")
	}
	raw, err := json.Marshal(provider.capturedResponsesReq.Input)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A chained turn replays the stored output, which holds what the client
// saw (restored values included): the prompt guardrails must see that
// history like an input the client replayed itself, or the provider gets
// it in clear.
func TestResponsesWithPreviousResponseID_HistoryPassesPromptGuardrails(t *testing.T) {
	for _, stream := range []bool{false, true} {
		provider := previousResponseTestProvider(t, "anthropic")
		provider.streamData = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_conv_2\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\ndata: [DONE]\n\n"
		patcher := &redactingPatcher{}
		srv := New(provider, &Config{TranslatedRequestPatcher: patcher})
		store := srv.handler.currentResponseStore()

		if rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"my pet is a zebra"}`); rec.Code != http.StatusOK {
			t.Fatalf("turn one status = %d (%s)", rec.Code, rec.Body.String())
		}
		if got := forwardedInput(t, provider.capturingProvider); strings.Contains(got, "zebra") {
			t.Fatalf("turn one forwarded unredacted: %s", got)
		}
		waitForStoredResponse(t, store, "resp_conv_1")

		body := `{"model":"gpt-5-mini","input":"what is it?","previous_response_id":"resp_conv_1","stream":` + map[bool]string{false: "false", true: "true"}[stream] + `}`
		rec := postResponses(t, srv, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("stream=%v chained status = %d (%s)", stream, rec.Code, rec.Body.String())
		}
		got := forwardedInput(t, provider.capturingProvider)
		if strings.Contains(got, "zebra") || strings.Count(got, "[animal]") != 2 {
			t.Fatalf("stream=%v chained turn forwarded %s, want the replayed input and output redacted", stream, got)
		}
		if seen := patcher.seen[len(patcher.seen)-1]; !strings.Contains(seen, "the word is zebra") {
			t.Fatalf("stream=%v prompt guardrail saw %s, want the replayed history", stream, seen)
		}
		if !stream && !strings.Contains(rec.Body.String(), `"previous_response_id":"resp_conv_1"`) {
			t.Fatalf("chained response must still echo previous_response_id: %s", rec.Body.String())
		}

		// Snapshots keep the client's own turn as sent, so the next replay
		// is anonymized consistently with the output.
		first, err := store.Get(context.Background(), "resp_conv_1")
		if err != nil {
			t.Fatal(err)
		}
		if len(first.InputItems) != 1 || !strings.Contains(string(first.InputItems[0]), "my pet is a zebra") {
			t.Fatalf("turn one snapshot input = %s, want the client's own input", first.InputItems)
		}
	}
}

func TestResponsesWithConversation_HistoryPassesPromptGuardrails(t *testing.T) {
	provider := conversationTestProvider(t)
	srv := New(provider, &Config{TranslatedRequestPatcher: &redactingPatcher{}})
	convID := createTestConversation(t, srv, `{"items":[{"type":"message","role":"user","content":"my pet is a zebra"}]}`)

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","conversation":"`+convID+`","input":"what is it?"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("responses status = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := forwardedInput(t, provider); strings.Contains(got, "zebra") || !strings.Contains(got, "[animal]") {
		t.Fatalf("conversation history forwarded %s, want it redacted", got)
	}
}

// With prompt guardrails on, a native primary with a chat-translated
// failover target gets the history expanded before the prompt phase too:
// expanding only at the failover attempt would skip the guardrails.
func TestResponsesWithPreviousResponseID_GuardedFailoverExpandsBeforePromptPhase(t *testing.T) {
	handler, provider := newChainingFailoverHandler(t)
	handler.translatedRequestPatcher = &redactingPatcher{} // read when the service is first built
	store := handler.currentResponseStore()
	if err := store.Update(context.Background(), &responsestore.StoredResponse{
		Response: &core.ResponsesResponse{
			ID: "resp_native", Object: "response", Status: "completed",
			Output: []core.ResponsesOutputItem{{ID: "msg_1", Type: "message", Role: "assistant", Content: []core.ResponsesContentItem{{Type: "output_text", Text: "a zebra"}}}},
		},
		InputItems: []json.RawMessage{json.RawMessage(`{"id":"in_1","type":"message","role":"user","content":[{"type":"input_text","text":"remember"}]}`)},
	}); err != nil {
		t.Fatalf("store: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-mini","input":"again?","previous_response_id":"resp_native"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := handler.Responses(echo.New().NewContext(req, rec)); err != nil {
		t.Fatalf("handler.Responses() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	fallback := provider.requests["anthropic/claude"]
	if fallback == nil || fallback.PreviousResponseID != "" {
		t.Fatalf("translated failover request = %#v, want the history expanded", fallback)
	}
	raw, _ := json.Marshal(fallback.Input)
	if strings.Contains(string(raw), "zebra") || !strings.Contains(string(raw), "[animal]") {
		t.Fatalf("translated failover forwarded %s, want the replayed output redacted", raw)
	}
}
