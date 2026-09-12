package core

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type testFileMultipartReader struct {
	values    map[string]string
	filenames map[string]string
}

func (r testFileMultipartReader) Value(name string) string {
	return r.values[name]
}

func (r testFileMultipartReader) Filename(name string) (string, bool) {
	value, ok := r.filenames[name]
	return value, ok
}

func TestDecodeChatRequest_CachesOnSemanticEnvelope(t *testing.T) {
	t.Parallel()

	env := &WhiteBoxPrompt{OperationType: "chat_completions"}
	first, err := DecodeChatRequest([]byte(`{"model":"gpt-4o-mini","provider":"openai","stream":true,"messages":[{"role":"user","content":"hi"}]}`), env)
	require.NoError(t, err)

	second, err := DecodeChatRequest([]byte(`{"model":"other","messages":[{"role":"user","content":"ignored"}]}`), env)
	require.NoError(t, err)
	require.Same(t, first, second)
	require.Same(t, first, env.CachedChatRequest())
	require.True(t, env.JSONBodyParsed)
	require.Equal(t, "gpt-4o-mini", env.RouteHints.Model)
	require.Equal(t, "openai", env.RouteHints.Provider)
	require.True(t, env.StreamRequested)
}

func TestBatchRouteMetadata_ValidatesAndCachesLimit(t *testing.T) {
	t.Parallel()

	env := &WhiteBoxPrompt{OperationType: "batches"}
	_, err := BatchRouteMetadata(env, "GET", "/v1/batches", nil, map[string][]string{
		"limit": {"bad"},
	})
	require.Error(t, err)

	req, err := BatchRouteMetadata(env, "GET", "/v1/batches", nil, map[string][]string{
		"after": {"batch_prev"},
		"limit": {"5"},
	})
	require.NoError(t, err)
	require.Same(t, env.CachedBatchRouteInfo(), req)
	require.Equal(t, BatchActionList, req.Action)
	require.True(t, req.HasLimit)
	require.Equal(t, 5, req.Limit)
}

func TestFileRouteMetadata_CachesProviderHint(t *testing.T) {
	t.Parallel()

	env := &WhiteBoxPrompt{OperationType: "files"}
	req, err := FileRouteMetadata(env, "GET", "/v1/files", nil, map[string][]string{
		"provider": {"openai"},
	})
	require.NoError(t, err)
	require.Same(t, env.CachedFileRouteInfo(), req)
	require.Equal(t, "openai", env.RouteHints.Provider)
}

func TestDecodeCanonicalSelector_UsesOperationCodec(t *testing.T) {
	t.Parallel()

	env := &WhiteBoxPrompt{OperationType: "responses"}
	model, provider, ok := DecodeCanonicalSelector([]byte(`{"model":"gpt-5-mini","provider":"openai","stream":true,"input":"hi"}`), env)
	require.True(t, ok)
	require.Equal(t, "gpt-5-mini", model)
	require.Equal(t, "openai", provider)
	require.NotNil(t, env.CachedResponsesRequest())
	require.True(t, env.StreamRequested)
}

func TestEnrichFileCreateRouteInfo_FillsMultipartMetadata(t *testing.T) {
	t.Parallel()

	req := &FileRouteInfo{Action: FileActionCreate}
	req = EnrichFileCreateRouteInfo(req, testFileMultipartReader{
		values: map[string]string{
			"provider": "openai",
			"purpose":  "batch",
		},
		filenames: map[string]string{
			"file": "requests.jsonl",
		},
	})

	require.Equal(t, "openai", req.Provider)
	require.Equal(t, "batch", req.Purpose)
	require.Equal(t, "requests.jsonl", req.Filename)
}
