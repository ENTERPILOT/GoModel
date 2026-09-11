// Package presidio is the built-in presidio plugin: it sends prompt and
// completion text to a Presidio analyzer sidecar, and anonymizes, flags, or
// blocks the personal data it finds. Anonymization happens in GoModel with
// numbered placeholders ("<PERSON_1>"), so the original values can be put
// back into the response, and no anonymizer service is needed.
package presidio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Name is the manifest name of the plugin.
const Name = "presidio"

var instances atomic.Int64

// Plugin is one configured presidio instance.
type Plugin struct {
	key    string // Exchange.Values key prefix, unique per instance
	host   pluginapi.Host
	client *client
	settings
}

// New returns an unconfigured plugin; call Init before use.
func New() pluginapi.Plugin { return &Plugin{} }

// Manifest describes the plugin and its configuration form.
func (p *Plugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{
		Name:        Name,
		Version:     "1.0.0",
		Description: "Detects personal data with a Presidio analyzer and anonymizes, flags, or blocks it in prompts, responses, and streams.",
		Kinds:       []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse, pluginapi.KindStream},
		Mutates:     true,
		Guardrail:   true,
		ConfigSchema: []pluginapi.Field{
			{
				Key: "analyzer_url", Label: "Analyzer URL", Input: pluginapi.InputText, Default: DefaultAnalyzerURL,
				Help:        "Base URL of the Presidio analyzer service (its /analyze endpoint is appended). No anonymizer service is needed.",
				Placeholder: DefaultAnalyzerURL,
			},
			{
				Key: "api_key", Label: "API key", Input: pluginapi.InputSecret,
				Help: "Optional bearer token sent as the Authorization header, for an analyzer behind an authenticating proxy. Presidio itself needs none. Needs an https:// analyzer URL unless it points at localhost.",
			},
			{
				Key: "language", Label: "Language", Input: pluginapi.InputText, Default: DefaultLanguage,
				Help:        "Two-letter language code the analyzer runs with. The analyzer must have a model for it; the default image ships only en.",
				Placeholder: DefaultLanguage,
			},
			{
				Key: "entities", Label: "Entity types", Input: pluginapi.InputList,
				Help:        "Entity types to look for, one per line (PERSON, EMAIL_ADDRESS, PHONE_NUMBER, CREDIT_CARD, ...). Empty looks for every type the analyzer supports.",
				Placeholder: "PERSON\nEMAIL_ADDRESS\nPHONE_NUMBER",
			},
			{
				Key: "block_entities", Label: "Blocking entity types", Input: pluginapi.InputList,
				Help:        "Entity types that reject the request (or answer with the message when the action is respond) instead of being anonymized or flagged, one per line. They are added to the entity types looked for.",
				Placeholder: "CREDIT_CARD\nUS_SSN",
			},
			{
				Key: "score_threshold", Label: "Score threshold", Input: pluginapi.InputNumber,
				Help:        "Minimum confidence (0 to 1) for a detection to count. Empty uses the analyzer's own threshold. Raise it when common words are mistaken for names or dates.",
				Placeholder: "analyzer default",
			},
			{
				Key: "allow_list", Label: "Allow list", Input: pluginapi.InputList,
				Help:        "Exact words or phrases that are never treated as personal data, one per line: product names, your company, well-known public figures.",
				Placeholder: "ACME Corp",
			},
			{
				Key: "ad_hoc_recognizers", Label: "Ad hoc recognizers", Input: pluginapi.InputTextarea,
				Help:        "JSON array of Presidio pattern recognizers sent with every request, each with supported_entity and patterns (regex with a score) or deny_list. Use it for identifiers of your own, such as customer or ticket numbers.",
				Placeholder: `[{"name": "ticket", "supported_entity": "TICKET_ID", "patterns": [{"name": "ticket", "regex": "TCK-\\d{6}", "score": 0.9}]}]`,
			},
			{
				Key: "roles", Label: "Prompt roles", Input: pluginapi.InputCheckboxes, Default: []string{"user", "assistant", "tool"},
				Help:    "Which prompt messages are analyzed, tool-result text included. Assistant covers earlier turns of the conversation, which carry restored values when restore is on. In the response phase the assistant text is always analyzed.",
				Options: pluginapi.RoleOptions(),
			},
			{
				Key: "action", Label: "On detection", Input: pluginapi.InputSelect, Default: ActionAnonymize,
				Help: "Anonymize rewrites the values with the operator. Block rejects with an error, respond answers with the message as an assistant reply, and warn continues while recording the entity types; none of these three edits the text.",
				Options: []pluginapi.Option{
					{Value: ActionAnonymize, Label: "Anonymize"},
					{Value: ActionBlock, Label: "Block"},
					{Value: ActionRespond, Label: "Respond"},
					{Value: ActionWarn, Label: "Warn"},
				},
			},
			{
				Key: "operator", Label: "Operator", Input: pluginapi.InputSelect, Default: OperatorReplace,
				Help: "How anonymize rewrites a value. Replace writes a numbered placeholder such as <PERSON_1>, the same one for every occurrence of that value in the request. Mask writes one * per character, redact removes the value, hash writes its SHA-256.",
				Options: []pluginapi.Option{
					{Value: OperatorReplace, Label: "Replace with <TYPE_n>"},
					{Value: OperatorMask, Label: "Mask with *"},
					{Value: OperatorRedact, Label: "Redact"},
					{Value: OperatorHash, Label: "Hash (SHA-256)"},
				},
			},
			{
				Key: "restore", Label: "Restore values in the response", Input: pluginapi.InputBool, Default: false,
				Help: "Put the original values back where the model repeats a placeholder, so the client sees its own data while the provider never does. Values from system and developer messages are never put back. Needs operator replace, and an instance with restore in the response or stream phase as well as in the prompt phase. Such responses are kept out of the response cache.",
			},
			{
				Key: "message", Label: "Message", Input: pluginapi.InputText, Default: DefaultMessage,
				Help:        "Error message for block, assistant reply for respond, and audit note for warn.",
				Placeholder: DefaultMessage,
			},
			pluginapi.BlockStatusField(),
			{
				Key: "stream_chunk", Label: "Stream chunk", Input: pluginapi.InputNumber, Default: DefaultStreamChunk,
				Help:        "Characters of streamed text collected before the analyzer is called on them, so it sees whole sentences and runs once per chunk rather than once per token. The client waits for at most this much text at a time. 0 analyzes every delta as it arrives.",
				Placeholder: fmt.Sprint(DefaultStreamChunk),
			},
			{
				Key: "stream_lookbehind", Label: "Stream lookbehind", Input: pluginapi.InputNumber, Default: DefaultStreamLookbehind,
				Help:        "Characters of streamed text held back so a value or placeholder split across two chunks is still handled; set it to at least the longest value you expect. Used by anonymize and warn. Block and respond buffer the whole stream instead, so nothing leaks before the decision.",
				Placeholder: fmt.Sprint(DefaultStreamLookbehind),
			},
		},
	}
}

// Init decodes and validates the instance configuration and prepares the
// analyzer client.
func (p *Plugin) Init(_ context.Context, raw json.RawMessage, host pluginapi.Host) error {
	s, err := decodeConfig(raw)
	if err != nil {
		return err
	}
	p.settings = s
	p.host = host
	httpClient := http.DefaultClient
	if host != nil && host.HTTPClient() != nil {
		httpClient = host.HTTPClient()
	}
	p.client = &client{http: httpClient, baseURL: s.analyzerURL, apiKey: s.apiKey, settings: &p.settings}
	p.key = fmt.Sprintf("%s:%d", Name, instances.Add(1))
	return nil
}

// Close releases nothing; the plugin holds no resources.
func (p *Plugin) Close(context.Context) error { return nil }

// Health asks the analyzer for the entity types of the configured language,
// which fails both when the service is down and when the language has no
// model.
func (p *Plugin) Health(ctx context.Context) error {
	return p.client.health(ctx)
}

// Summarize returns one line describing the instance for the dashboard list.
func (p *Plugin) Summarize(raw json.RawMessage) string {
	s, err := decodeConfig(raw)
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(s.action)
	if s.action == ActionAnonymize {
		fmt.Fprintf(&b, " (%s)", s.operator)
	}
	switch len(s.entities) {
	case 0:
		b.WriteString(", all entities")
	case 1:
		b.WriteString(", " + s.entities[0])
	default:
		fmt.Fprintf(&b, ", %d entity types", len(s.entities))
	}
	if len(s.blockEntities) > 0 {
		fmt.Fprintf(&b, ", %d blocking", len(s.blockEntities))
	}
	if s.restore {
		b.WriteString(", restore")
	}
	fmt.Fprintf(&b, ", %s", s.language)
	return b.String()
}
