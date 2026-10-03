package litellmmigrate

import "strings"

// providerKind describes how one LiteLLM provider prefix maps onto a GoModel
// provider type.
type providerKind struct {
	// Type is the GoModel provider type.
	Type string
	// Name is the base provider instance name. Empty uses Type.
	Name string
	// BaseURL is the base URL to set when the deployment has no api_base,
	// for providers GoModel reaches through its OpenAI-compatible adapter.
	BaseURL string
	// KeyEnv is the variable LiteLLM reads when a deployment sets no api_key.
	// Reusing it keeps an existing environment working unchanged.
	KeyEnv string
	// BaseEnv is the variable LiteLLM reads when a deployment sets no
	// api_base, kept only for provider types that cannot work without one.
	BaseEnv string
}

func (k providerKind) baseName() string {
	if k.Name != "" {
		return k.Name
	}
	return k.Type
}

// native reports whether the instance name is a GoModel provider type, whose
// bare <TYPE>_* environment variables override the provider of that name.
func (k providerKind) native() bool {
	return k.baseName() == k.Type
}

// providerKinds maps LiteLLM provider prefixes (the part of litellm_params.model
// before the first "/", or custom_llm_provider) to GoModel provider types.
var providerKinds = map[string]providerKind{
	"openai":                 {Type: "openai", KeyEnv: "OPENAI_API_KEY"},
	"text-completion-openai": {Type: "openai", KeyEnv: "OPENAI_API_KEY"},
	"litellm_proxy":          {Type: "openai", Name: "litellm-proxy", KeyEnv: "LITELLM_PROXY_API_KEY", BaseEnv: "LITELLM_PROXY_API_BASE"},
	"azure":                  {Type: "azure", KeyEnv: "AZURE_API_KEY", BaseEnv: "AZURE_API_BASE"},
	"azure_ai":               {Type: "openai", Name: "azure-ai", KeyEnv: "AZURE_AI_API_KEY", BaseEnv: "AZURE_AI_API_BASE"},
	"anthropic":              {Type: "anthropic", KeyEnv: "ANTHROPIC_API_KEY"},
	"gemini":                 {Type: "gemini", KeyEnv: "GEMINI_API_KEY"},
	"vertex_ai":              {Type: "vertex"},
	"bedrock":                {Type: "bedrock"},
	"groq":                   {Type: "groq", KeyEnv: "GROQ_API_KEY"},
	"fireworks_ai":           {Type: "fireworks", KeyEnv: "FIREWORKS_AI_API_KEY"},
	"xai":                    {Type: "xai", KeyEnv: "XAI_API_KEY"},
	"deepseek":               {Type: "deepseek", KeyEnv: "DEEPSEEK_API_KEY"},
	"openrouter":             {Type: "openrouter", KeyEnv: "OPENROUTER_API_KEY"},
	"cohere":                 {Type: "cohere", KeyEnv: "COHERE_API_KEY"},
	"cohere_chat":            {Type: "cohere", KeyEnv: "COHERE_API_KEY"},
	"ollama":                 {Type: "ollama", BaseURL: "http://localhost:11434"},
	"ollama_chat":            {Type: "ollama", BaseURL: "http://localhost:11434"},
	"hosted_vllm":            {Type: "vllm", KeyEnv: "HOSTED_VLLM_API_KEY", BaseEnv: "HOSTED_VLLM_API_BASE"},
	"vllm":                   {Type: "vllm"},
	"dashscope":              {Type: "bailian", KeyEnv: "DASHSCOPE_API_KEY"},
	"zai":                    {Type: "zai", KeyEnv: "ZAI_API_KEY"},
	"minimax":                {Type: "minimax", KeyEnv: "MINIMAX_API_KEY"},
	"meta_llama":             {Type: "meta", KeyEnv: "LLAMA_API_KEY"},
	"llamafile":              {Type: "openai", Name: "llamafile", BaseURL: "http://127.0.0.1:8080/v1"},
	"lm_studio":              {Type: "openai", Name: "lm-studio", BaseURL: "http://localhost:1234/v1"},
	"mistral":                {Type: "openai", Name: "mistral", BaseURL: "https://api.mistral.ai/v1", KeyEnv: "MISTRAL_API_KEY"},
	"together_ai":            {Type: "openai", Name: "together-ai", BaseURL: "https://api.together.xyz/v1", KeyEnv: "TOGETHERAI_API_KEY"},
	"perplexity":             {Type: "openai", Name: "perplexity", BaseURL: "https://api.perplexity.ai", KeyEnv: "PERPLEXITYAI_API_KEY"},
	"deepinfra":              {Type: "openai", Name: "deepinfra", BaseURL: "https://api.deepinfra.com/v1/openai", KeyEnv: "DEEPINFRA_API_KEY"},
	"cerebras":               {Type: "openai", Name: "cerebras", BaseURL: "https://api.cerebras.ai/v1", KeyEnv: "CEREBRAS_API_KEY"},
	"nvidia_nim":             {Type: "openai", Name: "nvidia-nim", BaseURL: "https://integrate.api.nvidia.com/v1", KeyEnv: "NVIDIA_NIM_API_KEY"},
	"sambanova":              {Type: "openai", Name: "sambanova", BaseURL: "https://api.sambanova.ai/v1", KeyEnv: "SAMBANOVA_API_KEY"},
	"moonshot":               {Type: "openai", Name: "moonshot", BaseURL: "https://api.moonshot.ai/v1", KeyEnv: "MOONSHOT_API_KEY"},
}

// splitModel separates litellm_params.model into the provider prefix and the
// upstream model ID. custom_llm_provider, when set, wins over the prefix, and
// a model with no known prefix falls back to LiteLLM's own name inference for
// the two families it recognises without one.
func splitModel(model, customProvider string) (prefix, upstream string) {
	model = strings.TrimSpace(model)
	if model == "*" {
		return "*", "*"
	}
	if custom := strings.TrimSpace(customProvider); custom != "" {
		if rest, ok := strings.CutPrefix(model, custom+"/"); ok {
			return custom, rest
		}
		return custom, model
	}
	if before, after, ok := strings.Cut(model, "/"); ok {
		if _, known := providerKinds[before]; known {
			return before, after
		}
		if before == "*" || after == "*" {
			return before, after
		}
	}
	return inferPrefix(model), model
}

// inferPrefix mirrors LiteLLM's provider inference for bare model names.
func inferPrefix(model string) string {
	lower := strings.ToLower(model)
	for _, prefix := range []string{"gpt-", "o1", "o3", "o4", "chatgpt-", "text-embedding-", "dall-e", "gpt-image", "whisper-", "tts-", "omni-moderation", "text-moderation"} {
		if strings.HasPrefix(lower, prefix) {
			return "openai"
		}
	}
	if strings.HasPrefix(lower, "claude-") {
		return "anthropic"
	}
	return ""
}

// normalizeUpstream rewrites LiteLLM route hints that are not part of the
// upstream model ID.
func normalizeUpstream(prefix, upstream string) string {
	if prefix == "bedrock" {
		for _, route := range []string{"converse/", "invoke/"} {
			upstream = strings.TrimPrefix(upstream, route)
		}
	}
	return upstream
}

// normalizeBaseURL adapts a LiteLLM api_base to the GoModel provider type.
// LiteLLM appends API paths that GoModel expects in the base URL: Anthropic's
// /v1, the /v1 of OpenAI-compatible local servers, and Azure's deployment
// path (GoModel binds one Azure provider to one deployment).
func normalizeBaseURL(kind providerKind, base, deployment string) string {
	if base == "" {
		return ""
	}
	trimmed := strings.TrimRight(base, "/")
	switch kind.Type {
	case "anthropic":
		trimmed = strings.TrimSuffix(strings.TrimSuffix(trimmed, "/messages"), "/v1")
		return trimmed + "/v1"
	case "ollama", "vllm":
		if !strings.HasSuffix(trimmed, "/v1") {
			return trimmed + "/v1"
		}
		return trimmed
	case "azure":
		if strings.Contains(trimmed, "/deployments/") {
			return trimmed
		}
		return strings.TrimSuffix(trimmed, "/openai") + "/openai/deployments/" + deployment
	}
	return base
}

// slug turns a model or deployment name into a provider name fragment.
func slug(value string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}
