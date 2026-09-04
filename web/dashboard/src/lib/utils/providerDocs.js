// Docs URLs for every registered provider type.
//
// Source of truth:
//   - Provider registry: run/providers.go (the list of types registered into
//     the factory at startup, e.g. openai, opencode_go, bedrock-mantle).
//   - Docs slugs: docs/providers/*.mdx + docs/docs.json. The list below is
//     derived from those files, not hand-typed as per-provider URLs.
//
// Behaviour:
//   - Normalizes the registry type (trim, lowercase, underscores -> dashes)
//     so e.g. opencode_go resolves to the opencode-go docs page.
//   - Types with a dedicated docs page link to it.
//   - Types registered but without a dedicated page (openai, ollama, groq,
//     kilo, zai, ...) fall back to the providers/overview page so every
//     provider card still surfaces a help link.
//   - Empty/whitespace input returns "" so callers can gate rendering.

export const PROVIDER_DOCS_BASE_URL =
  "https://gomodel.enterpilot.io/docs/providers/";
export const PROVIDER_DOCS_UTM = "utm_source=gomodel_dashboard";

// Provider docs slugs derived from docs/providers/*.mdx.
const PROVIDER_DOC_SLUGS = new Set([
  "anthropic",
  "azure",
  "bailian",
  "bedrock",
  "bedrock-mantle",
  "chatgpt",
  "cohere",
  "deepseek",
  "elevenlabs",
  "gemini",
  "hetzner",
  "kimicode",
  "llamacpp",
  "llmd",
  "minimax",
  "opencode-go",
  "oracle",
  "sglang",
  "vertex",
  "vllm",
  "xai",
  "xiaomi",
]);

// Normalized registry type → docs slug, for the cases where the docs page
// slug does not match the provider type. `ollama` is documented under the
// "multiple-ollama" page (its frontmatter title is "Ollama").
const PROVIDER_DOC_SLUG_OVERRIDES = {
  ollama: "multiple-ollama",
};

export const PROVIDER_DOCS_OVERVIEW_SLUG = "overview";

function normalizeProviderType(type) {
  return String(type || "")
    .trim()
    .toLowerCase()
    .replaceAll("_", "-");
}

export function providerDocsUrl(type) {
  const normalized = normalizeProviderType(type);
  if (!normalized) {
    return "";
  }
  const override = PROVIDER_DOC_SLUG_OVERRIDES[normalized];
  const slug =
    override ??
    (PROVIDER_DOC_SLUGS.has(normalized)
      ? normalized
      : PROVIDER_DOCS_OVERVIEW_SLUG);
  return PROVIDER_DOCS_BASE_URL + slug + "?" + PROVIDER_DOCS_UTM;
}
