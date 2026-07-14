package config

// GuardrailsConfig holds configuration for the request guardrails pipeline.
type GuardrailsConfig struct {
	// Enabled controls whether guardrails are active
	// Default: false
	Enabled bool `yaml:"enabled" env:"GUARDRAILS_ENABLED"`

	// EnableForBatchProcessing controls whether guardrails are applied to inline
	// batch items for /v1/batches requests.
	// Default: false
	EnableForBatchProcessing bool `yaml:"enable_for_batch_processing" env:"ENABLE_GUARDRAILS_FOR_BATCH_PROCESSING"`

	// Rules is a list of guardrail instances. Each entry defines one guardrail
	// with its own name, type, order, and type-specific settings. Multiple
	// instances of the same type are allowed (e.g. two system_prompt guardrails
	// with different content).
	Rules []GuardrailRuleConfig `yaml:"rules"`
}

// GuardrailRuleConfig defines a single guardrail instance.
type GuardrailRuleConfig struct {
	// Name is a unique identifier for this guardrail instance (used in logs and errors)
	Name string `yaml:"name"`

	// Type selects the guardrail implementation: "system_prompt",
	// "llm_based_altering", or "header_modification"
	Type string `yaml:"type"`

	// UserPath scopes internal auxiliary guardrail requests for workflow
	// selection and audit logging. When empty, the caller user path is used.
	UserPath string `yaml:"user_path"`

	// Order controls execution ordering relative to other guardrails.
	// Guardrails with the same order run in parallel; different orders run sequentially.
	// Default: 0
	Order int `yaml:"order"`

	// SystemPrompt holds settings when Type is "system_prompt"
	SystemPrompt SystemPromptSettings `yaml:"system_prompt"`

	// LLMBasedAltering holds settings when Type is "llm_based_altering"
	LLMBasedAltering LLMBasedAlteringSettings `yaml:"llm_based_altering"`

	// HeaderModification holds settings when Type is "header_modification"
	HeaderModification HeaderModificationSettings `yaml:"header_modification"`
}

// SystemPromptSettings holds the type-specific settings for a system_prompt guardrail.
type SystemPromptSettings struct {
	// Mode controls how the system prompt is applied: "inject", "override", or "decorator"
	//   - inject: adds a system message only if none exists
	//   - override: replaces all existing system messages
	//   - decorator: prepends to the first existing system message
	// Default: "inject"
	Mode string `yaml:"mode"`

	// Content is the system prompt text to apply
	Content string `yaml:"content"`
}

// LLMBasedAlteringSettings holds the type-specific settings for an llm_based_altering guardrail.
type LLMBasedAlteringSettings struct {
	// Model is the model selector used for the auxiliary rewrite call.
	// This can be a concrete model name, provider-qualified selector, or alias.
	Model string `yaml:"model"`

	// Provider is an optional routing hint for Model.
	Provider string `yaml:"provider"`

	// Prompt is the system prompt used to rewrite targeted messages.
	// When empty, the built-in LiteLLM-derived anonymization prompt is used.
	Prompt string `yaml:"prompt"`

	// Roles selects which message roles are rewritten.
	// Default: ["user"]
	Roles []string `yaml:"roles"`

	// SkipContentPrefix skips rewriting for messages whose trimmed text begins with this prefix.
	SkipContentPrefix string `yaml:"skip_content_prefix"`

	// MaxTokens limits the auxiliary rewrite completion.
	// Default: 4096
	MaxTokens int `yaml:"max_tokens"`
}

// HeaderModificationSettings holds the type-specific settings for a
// header_modification guardrail: conditions over inbound request headers and
// the outbound provider-request header changes to apply when they all match.
type HeaderModificationSettings struct {
	// When lists inbound-header conditions; all must match. Empty = always apply.
	When []HeaderConditionConfig `yaml:"when"`

	// Actions lists outbound header changes applied in order.
	Actions []HeaderActionConfig `yaml:"actions"`
}

// HeaderConditionConfig is one inbound-header predicate.
type HeaderConditionConfig struct {
	// Header is the inbound header name to inspect.
	Header string `yaml:"header"`

	// Equals matches when any inbound value equals this string exactly.
	Equals *string `yaml:"equals"`

	// Matches matches when any inbound value matches this RE2 regular expression.
	Matches *string `yaml:"matches"`

	// Present requires the header to exist (true) or be absent (false).
	// Ignored when Equals or Matches is set; defaults to true otherwise.
	Present *bool `yaml:"present"`
}

// HeaderActionConfig is one outbound-header change.
type HeaderActionConfig struct {
	// Action is "set" (replace/add) or "remove".
	Action string `yaml:"action"`

	// Header is the outbound header to change. Credential and transport
	// headers (Authorization, Cookie, Host, Content-Length, ...) are rejected.
	Header string `yaml:"header"`

	// Value is the literal value for "set".
	Value string `yaml:"value"`

	// FromHeader copies the first inbound value of this header for "set".
	// When the inbound header is absent, the action is skipped.
	FromHeader string `yaml:"from_header"`
}
