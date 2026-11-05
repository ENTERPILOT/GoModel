package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/requestparams"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// Two aliases of one self-hosted model carry their own sampling settings
// through the real virtualmodels.Service: forced params replace the client's
// values, defaults fill in what the client left out, and a failover leg to
// another model is rebuilt from the client's request with that leg's own
// params.
func TestChatCompletion_VirtualModelParams(t *testing.T) {
	catalog := &aliasesTestCatalog{
		supported:     map[string]bool{"vllm/qwen": true, "vllm/backup": true},
		providerTypes: map[string]string{"vllm/qwen": "vllm", "vllm/backup": "vllm"},
	}
	rows := []virtualmodels.VirtualModel{
		{
			Source:          "qwen-thinking-low",
			Targets:         []virtualmodels.Target{{Model: "vllm/qwen"}},
			ReasoningEffort: "low",
			Params:          requestparams.Set{"chat_template_kwargs": json.RawMessage(`{"enable_thinking":true,"preserve_thinking":true}`)},
			DefaultParams:   requestparams.Set{"temperature": json.RawMessage(`1`), "top_k": json.RawMessage(`20`), "min_p": json.RawMessage(`0`)},
			Enabled:         true,
		},
		{
			Source:   "qwen-fast",
			Strategy: virtualmodels.StrategyFailover,
			Targets:  []virtualmodels.Target{{Model: "vllm/qwen"}, {Model: "vllm/backup"}},
			Params:   requestparams.Set{"chat_template_kwargs": json.RawMessage(`{"enable_thinking":false}`), "top_k": nil},
			Enabled:  true,
		},
		{
			Source: "vllm/backup", ProviderName: "vllm", Model: "backup",
			Params:  requestparams.Set{"repetition_penalty": json.RawMessage(`1.1`)},
			Enabled: true,
		},
	}
	service, err := virtualmodels.NewService(newAliasesTestStore(rows...), catalog, true)
	require.NoError(t, err)
	require.NoError(t, service.Refresh(context.Background()))

	ok := &core.ChatResponse{ID: "chatcmpl-1", Object: "chat.completion", Model: "qwen", Choices: []core.Choice{{Message: core.ResponseMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}}}
	provider := &failoverProvider{
		chatResponses:   map[string]*core.ChatResponse{"vllm/qwen": ok, "vllm/backup": ok},
		supportedModels: map[string]string{"vllm/qwen": "vllm", "vllm/backup": "vllm"},
	}
	handler := newHandler(provider, nil, nil, nil, service, nil, service, nil)

	post := func(body string) {
		t.Helper()
		c, rec := echotest.Post(t, "/v1/chat/completions", body)
		c.Set(string(auditlog.LogEntryKey), &auditlog.LogEntry{Data: &auditlog.LogData{}})
		require.NoError(t, handler.ChatCompletion(c))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	post(`{"model":"qwen-thinking-low","messages":[{"role":"user","content":"hi"}],"temperature":0.2,"chat_template_kwargs":{"preserve_thinking":false,"extra":1}}`)
	require.Len(t, provider.chatRequests, 1)
	sent := provider.chatRequests[0]
	assert.Equal(t, "qwen", sent.Model)
	require.NotNil(t, sent.Temperature)
	assert.Equal(t, 0.2, *sent.Temperature, "a default never replaces the client's value")
	assert.JSONEq(t, `20`, string(sent.ExtraFields.Lookup("top_k")))
	assert.JSONEq(t, `0`, string(sent.ExtraFields.Lookup("min_p")))
	assert.JSONEq(t, `{"enable_thinking":true,"preserve_thinking":true,"extra":1}`, string(sent.ExtraFields.Lookup("chat_template_kwargs")), "objects merge one level deep with the alias winning")
	require.NotNil(t, sent.Reasoning)
	assert.Equal(t, "low", sent.Reasoning.Effort)

	provider.chatErrors = map[string]error{"vllm/qwen": core.NewProviderError("vllm", http.StatusServiceUnavailable, "down", nil)}
	post(`{"model":"qwen-fast","messages":[{"role":"user","content":"hi"}],"top_k":5,"seed":7}`)
	require.Len(t, provider.chatRequests, 3)
	primary, fallback := provider.chatRequests[1], provider.chatRequests[2]
	assert.Equal(t, "qwen", primary.Model)
	assert.Nil(t, primary.ExtraFields.Lookup("top_k"), "null strips the client's key")
	assert.JSONEq(t, `{"enable_thinking":false}`, string(primary.ExtraFields.Lookup("chat_template_kwargs")))
	assert.JSONEq(t, `7`, string(primary.ExtraFields.Lookup("seed")))
	assert.Nil(t, primary.Reasoning, "no effort configured on this alias")

	assert.Equal(t, "backup", fallback.Model)
	assert.Nil(t, fallback.ExtraFields.Lookup("top_k"), "the alias params apply to every leg of the balancer")
	assert.JSONEq(t, `{"enable_thinking":false}`, string(fallback.ExtraFields.Lookup("chat_template_kwargs")))
	assert.JSONEq(t, `1.1`, string(fallback.ExtraFields.Lookup("repetition_penalty")), "the model's own policy adds its params")
	assert.JSONEq(t, `7`, string(fallback.ExtraFields.Lookup("seed")))
}
