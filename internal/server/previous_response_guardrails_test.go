package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
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

// inspectingPatcher stands in for a prompt guardrail that only classifies:
// it reads the request but never rewrites it, so a chained request keeps the
// native passthrough.
type inspectingPatcher struct{ seen []string }

func (p *inspectingPatcher) PatchChatRequest(_ context.Context, req *core.ChatRequest) (*core.ChatRequest, error) {
	return req, nil
}

func (p *inspectingPatcher) PatchResponsesRequest(_ context.Context, req *core.ResponsesRequest) (*core.ResponsesRequest, error) {
	raw, err := json.Marshal(req.Input)
	if err != nil {
		return nil, err
	}
	p.seen = append(p.seen, string(raw))
	return req, nil
}

func (p *inspectingPatcher) EditsPromptContent(context.Context) bool { return false }

func forwardedInput(t *testing.T, provider *capturingProvider) string {
	t.Helper()
	require.NotNil(t, provider.capturedResponsesReq)

	raw, err := json.Marshal(provider.capturedResponsesReq.Input)
	require.NoError(t, err)

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
		rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"my pet is a zebra"}`)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		got := forwardedInput(t, provider.capturingProvider)
		require.NotContains(t, got, "zebra", "turn one forwarded unredacted: %s", got)

		waitForStoredResponse(t, store, "resp_conv_1")

		provider.responsesResponse.ID = "resp_conv_2" // the streamed fixture carries resp_conv_2 too
		body := `{"model":"gpt-5-mini","input":"what is it?","previous_response_id":"resp_conv_1","stream":` + map[bool]string{false: "false", true: "true"}[stream] + `}`
		rec = postResponses(t, srv, body)
		require.Equal(t, http.StatusOK, rec.Code, "stream=%v chained status = %d (%s)", stream, rec.Code, rec.Body.String())

		got = forwardedInput(t, provider.capturingProvider)
		require.NotContains(t, got, "zebra")
		require.Equal(t, 2, strings.Count(got, "[animal]"), "stream=%v chained turn forwarded %s, want the replayed input and output redacted", stream, got)
		seen := patcher.seen[len(patcher.seen)-1]
		require.Contains(t, seen, "the word is zebra", "stream=%v prompt guardrail saw %s, want the replayed history", stream, seen)

		if !stream {
			require.Contains(t, rec.Body.String(), `"previous_response_id":"resp_conv_1"`, "chained response must still echo previous_response_id")
		}

		// Snapshots keep the client's own turn as sent, so the next replay
		// is anonymized consistently with the output, and link to their
		// predecessor instead of holding the replayed history.
		first, err := store.Get(context.Background(), "resp_conv_1")
		require.NoError(t, err)
		require.Len(t, first.InputItems, 1)
		require.Contains(t, string(first.InputItems[0]), "my pet is a zebra", "stream=%v turn one snapshot input = %s, want the client's own input", stream, first.InputItems)

		waitForStoredResponse(t, store, "resp_conv_2")
		second, err := store.Get(context.Background(), "resp_conv_2")
		require.NoError(t, err)
		require.Equal(t, "resp_conv_1", second.Response.PreviousResponseID)
		require.Len(t, second.InputItems, 1)
		require.Contains(t, string(second.InputItems[0]), "what is it?", "stream=%v chained snapshot previous=%q input=%s, want resp_conv_1 and only the client's own turn", stream, second.Response.PreviousResponseID, second.InputItems)
	}
}

func TestResponsesWithConversation_HistoryPassesPromptGuardrails(t *testing.T) {
	provider := conversationTestProvider(t)
	srv := New(provider, &Config{TranslatedRequestPatcher: &redactingPatcher{}})
	convID := createTestConversation(t, srv, `{"items":[{"type":"message","role":"user","content":"my pet is a zebra"}]}`)

	rec := postResponses(t, srv, `{"model":"gpt-5-mini","conversation":"`+convID+`","input":"what is it?"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := forwardedInput(t, provider)
	require.NotContains(t, got, "zebra")
	require.Contains(t, got, "[animal]")
}

// A native primary resolves previous_response_id itself, so a rewriting
// prompt guardrail would never see the earlier turns: an anonymizing
// guardrail would hand the placeholders of that history to new values and
// restore another turn's data into the answer. The history is expanded for a
// native primary too while such a guardrail runs, and the id goes with it so
// the provider does not replay the same turns twice.
func TestResponsesWithPreviousResponseID_NativePrimaryExpandsForEditingGuardrail(t *testing.T) {
	for _, stream := range []bool{false, true} {
		provider := previousResponseTestProvider(t, "openai")
		provider.streamData = streamedResponseData("resp_conv_2", "still zebra")
		patcher := &redactingPatcher{}
		srv := New(provider, &Config{TranslatedRequestPatcher: patcher})
		store := srv.handler.currentResponseStore()
		rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"my pet is a zebra"}`)
		require.Equal(t, http.StatusOK, rec.Code, "stream=%v turn one status = %d (%s)", stream, rec.Code, rec.Body.String())

		waitForStoredResponse(t, store, "resp_conv_1")

		provider.responsesResponse.ID = "resp_conv_2"
		body := `{"model":"gpt-5-mini","input":"what is it?","previous_response_id":"resp_conv_1","stream":` + map[bool]string{false: "false", true: "true"}[stream] + `}`
		rec = postResponses(t, srv, body)
		require.Equal(t, http.StatusOK, rec.Code, "stream=%v chained status = %d (%s)", stream, rec.Code, rec.Body.String())

		forwarded := provider.capturedResponsesReq
		require.Empty(t, forwarded.PreviousResponseID, "stream=%v expanded history must not also carry previous_response_id, got %q", stream, forwarded.PreviousResponseID)

		items := forwardedInputItems(t, provider.capturingProvider)
		require.Len(t, items, 3)
		got := forwardedInput(t, provider.capturingProvider)
		require.NotContains(t, got, "zebra")
		require.Equal(t, 2, strings.Count(got, "[animal]"), "stream=%v chained turn forwarded %s, want the replayed input and output redacted", stream, got)
		seen := patcher.seen[len(patcher.seen)-1]
		require.Contains(t, seen, "the word is zebra", "stream=%v prompt guardrail saw %s, want the replayed history", stream, seen)

		if !stream {
			require.Contains(t, rec.Body.String(), `"previous_response_id":"resp_conv_1"`, "chained response must still echo previous_response_id")
		}
	}
}

// A prompt guardrail that only classifies leaves the native passthrough
// alone: the provider keeps resolving previous_response_id itself, so its
// own history stays cached upstream.
func TestResponsesWithPreviousResponseID_NativePrimaryKeepsIDForInspectingGuardrail(t *testing.T) {
	provider := previousResponseTestProvider(t, "openai")
	patcher := &inspectingPatcher{}
	srv := New(provider, &Config{TranslatedRequestPatcher: patcher})
	store := srv.handler.currentResponseStore()
	rec := postResponses(t, srv, `{"model":"gpt-5-mini","input":"my pet is a zebra"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	waitForStoredResponse(t, store, "resp_conv_1")

	provider.responsesResponse.ID = "resp_conv_2"
	rec = postResponses(t, srv, `{"model":"gpt-5-mini","input":"what is it?","previous_response_id":"resp_conv_1"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	forwarded := provider.capturedResponsesReq
	require.Equal(t, "resp_conv_1", forwarded.PreviousResponseID)
	_, ok := forwarded.Input.(string)
	require.True(t, ok, "native input must be untouched, got %#v", forwarded.Input)
}

// With prompt guardrails on, a native primary with a chat-translated
// failover target gets the history expanded before the prompt phase too:
// expanding only at the failover attempt would skip the guardrails.
func TestResponsesWithPreviousResponseID_GuardedFailoverExpandsBeforePromptPhase(t *testing.T) {
	handler, provider := newChainingFailoverHandler(t)
	handler.translatedRequestPatcher = &redactingPatcher{} // read when the service is first built
	store := handler.currentResponseStore()
	err := store.Update(context.Background(), &responsestore.StoredResponse{
		Response: &core.ResponsesResponse{
			ID: "resp_native", Object: "response", Status: "completed",
			Output: []core.ResponsesOutputItem{{ID: "msg_1", Type: "message", Role: "assistant", Content: []core.ResponsesContentItem{{Type: "output_text", Text: "a zebra"}}}},
		},
		InputItems: []json.RawMessage{json.RawMessage(`{"id":"in_1","type":"message","role":"user","content":[{"type":"input_text","text":"remember"}]}`)},
	})
	require.NoError(t, err)

	c, rec := echotest.Post(t, "/v1/responses", `{"model":"gpt-5-mini","input":"again?","previous_response_id":"resp_native"}`)
	err = handler.Responses(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	fallback := provider.requests["anthropic/claude"]
	require.NotNil(t, fallback)
	require.Empty(t, fallback.PreviousResponseID)

	raw, _ := json.Marshal(fallback.Input)
	require.NotContains(t, string(raw), "zebra")
	require.Contains(t, string(raw), "[animal]", "translated failover forwarded %s, want the replayed output redacted", raw)
}
