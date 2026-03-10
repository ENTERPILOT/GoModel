package core

import "testing"

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

	env := &SemanticEnvelope{Operation: "chat_completions"}
	first, err := DecodeChatRequest([]byte(`{"model":"gpt-4o-mini","provider":"openai","messages":[{"role":"user","content":"hi"}]}`), env)
	if err != nil {
		t.Fatalf("DecodeChatRequest() error = %v", err)
	}
	second, err := DecodeChatRequest([]byte(`{"model":"other","messages":[{"role":"user","content":"ignored"}]}`), env)
	if err != nil {
		t.Fatalf("DecodeChatRequest() second error = %v", err)
	}
	if first != second {
		t.Fatal("DecodeChatRequest() did not reuse cached request")
	}
	if env.CachedChatRequest() != first {
		t.Fatal("SemanticEnvelope cached chat request was not reused")
	}
	if !env.JSONBodyParsed {
		t.Fatal("JSONBodyParsed = false, want true")
	}
	if env.SelectorHints.Model != "gpt-4o-mini" {
		t.Fatalf("SelectorHints.Model = %q, want gpt-4o-mini", env.SelectorHints.Model)
	}
	if env.SelectorHints.Provider != "openai" {
		t.Fatalf("SelectorHints.Provider = %q, want openai", env.SelectorHints.Provider)
	}
}

func TestBatchRouteMetadata_ValidatesAndCachesLimit(t *testing.T) {
	t.Parallel()

	env := &SemanticEnvelope{Operation: "batches"}
	_, err := BatchRouteMetadata(env, "GET", "/v1/batches", nil, map[string][]string{
		"limit": {"bad"},
	})
	if err == nil {
		t.Fatal("BatchRouteMetadata() error = nil, want invalid limit error")
	}

	req, err := BatchRouteMetadata(env, "GET", "/v1/batches", nil, map[string][]string{
		"after": {"batch_prev"},
		"limit": {"5"},
	})
	if err != nil {
		t.Fatalf("BatchRouteMetadata() valid error = %v", err)
	}
	if req != env.CachedBatchMetadata() {
		t.Fatal("BatchRouteMetadata() did not cache metadata on envelope")
	}
	if req.Action != BatchActionList {
		t.Fatalf("Action = %q, want %q", req.Action, BatchActionList)
	}
	if !req.HasLimit || req.Limit != 5 {
		t.Fatalf("limit = %d/%v, want 5/true", req.Limit, req.HasLimit)
	}
}

func TestFileRouteMetadata_CachesProviderHint(t *testing.T) {
	t.Parallel()

	env := &SemanticEnvelope{Operation: "files"}
	req, err := FileRouteMetadata(env, "GET", "/v1/files", nil, map[string][]string{
		"provider": {"openai"},
	})
	if err != nil {
		t.Fatalf("FileRouteMetadata() error = %v", err)
	}
	if req != env.CachedFileRequest() {
		t.Fatal("FileRouteMetadata() did not cache metadata on envelope")
	}
	if env.SelectorHints.Provider != "openai" {
		t.Fatalf("SelectorHints.Provider = %q, want openai", env.SelectorHints.Provider)
	}
}

func TestNormalizeModelSelector_UpdatesSemanticHints(t *testing.T) {
	t.Parallel()

	env := &SemanticEnvelope{
		SelectorHints: SelectorHints{
			Model:    "openai/gpt-4o-mini",
			Provider: "",
		},
	}
	model := "openai/gpt-4o-mini"
	provider := ""

	err := NormalizeModelSelector(env, &model, &provider)
	if err != nil {
		t.Fatalf("NormalizeModelSelector() error = %v", err)
	}

	if model != "gpt-4o-mini" {
		t.Fatalf("model = %q, want gpt-4o-mini", model)
	}
	if provider != "openai" {
		t.Fatalf("provider = %q, want openai", provider)
	}
	if env.SelectorHints.Model != "gpt-4o-mini" {
		t.Fatalf("SelectorHints.Model = %q, want gpt-4o-mini", env.SelectorHints.Model)
	}
	if env.SelectorHints.Provider != "openai" {
		t.Fatalf("SelectorHints.Provider = %q, want openai", env.SelectorHints.Provider)
	}
}

func TestDecodeCanonicalSelector_UsesOperationCodec(t *testing.T) {
	t.Parallel()

	env := &SemanticEnvelope{Operation: "responses"}
	model, provider, ok := DecodeCanonicalSelector([]byte(`{"model":"gpt-5-mini","provider":"openai","input":"hi"}`), env)
	if !ok {
		t.Fatal("DecodeCanonicalSelector() ok = false, want true")
	}
	if model != "gpt-5-mini" {
		t.Fatalf("model = %q, want gpt-5-mini", model)
	}
	if provider != "openai" {
		t.Fatalf("provider = %q, want openai", provider)
	}
	if env.CachedResponsesRequest() == nil {
		t.Fatal("ResponsesRequest was not cached on semantic envelope")
	}
}

func TestEnrichFileCreateRequestSemantic_FillsMultipartMetadata(t *testing.T) {
	t.Parallel()

	req := &FileRequestSemantic{Action: FileActionCreate}
	req = EnrichFileCreateRequestSemantic(req, testFileMultipartReader{
		values: map[string]string{
			"provider": "openai",
			"purpose":  "batch",
		},
		filenames: map[string]string{
			"file": "requests.jsonl",
		},
	})

	if req.Provider != "openai" {
		t.Fatalf("Provider = %q, want openai", req.Provider)
	}
	if req.Purpose != "batch" {
		t.Fatalf("Purpose = %q, want batch", req.Purpose)
	}
	if req.Filename != "requests.jsonl" {
		t.Fatalf("Filename = %q, want requests.jsonl", req.Filename)
	}
}
