// Package inferencehook is the built-in inference_hook plugin: it sends the
// conversation to an AI security server that speaks Anthropic's Inference
// hooks protocol and blocks, answers, or flags the exchange on a deny
// verdict. GoModel is the caller here, the role Anthropic's servers play for
// Claude Enterprise: a signed POST carries the transcript (a "prompt" frame
// before the provider call, a "tool_call" frame when a response holds tool
// calls) and the server answers {"action": "allow"} or {"action": "deny"}.
//
// Protocol: https://platform.claude.com/docs/en/manage-claude/inference-hooks-endpoint
package inferencehook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Name is the manifest name of the plugin.
const Name = "inference_hook"

// Plugin is one configured inference_hook instance.
type Plugin struct {
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
		Description: "Asks an AI security server that speaks Anthropic's Inference hooks protocol for an allow or deny verdict on prompts and tool calls, then blocks, answers, or flags them.",
		Kinds:       []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse, pluginapi.KindStream},
		Mutates:     false,
		Guardrail:   true,
		ConfigSchema: []pluginapi.Field{
			{
				Key: "endpoint_url", Label: "Endpoint URL", Input: pluginapi.InputText, Required: true, Destination: true,
				Help:        "URL of the AI security server. The whole URL is the endpoint: nothing is appended, and redirects are not followed. Use https:// unless the server is on localhost or a private network you trust with the conversation.",
				Placeholder: "https://security.example.com/hooks/verdict",
			},
			{
				Key: "signing_secret", Label: "Signing secret", Input: pluginapi.InputSecret, Required: true,
				Help:        "Standard Webhooks secret every request is signed with: base64, with or without the whsec_ prefix. Configure the same value on the security server so it can verify the webhook-signature header.",
				Placeholder: "whsec_...",
			},
			{
				Key: "auth_header", Label: "Auth header", Input: pluginapi.InputText, Default: DefaultAuthHeader,
				Help:        "Name of the static header that carries the auth value, for a security server that also authenticates its callers.",
				Placeholder: DefaultAuthHeader,
			},
			{
				Key: "auth_value", Label: "Auth value", Input: pluginapi.InputSecret,
				Help:        "Optional value of the auth header, sent as written, for example \"Bearer <token>\". Needs an https:// endpoint URL unless it points at localhost.",
				Placeholder: "Bearer ...",
			},
			{
				Key: "tenant_id", Label: "Tenant ID", Input: pluginapi.InputText,
				Help: "Optional tenant_id sent with every request, for a security server that serves several organizations. Empty sends null.",
			},
			{
				Key: "roles", Label: "Prompt roles", Input: pluginapi.InputCheckboxes, Default: []string{"system", "user", "assistant", "tool"},
				Help:    "Which prompt messages are sent in the prompt phase. Anthropic's own frames never carry system prompts, but through a gateway the caller writes them, so they are sent by default, under the user role because the protocol has no system role. Uncheck system when the security server should see what it sees from Claude Enterprise.",
				Options: pluginapi.RoleOptions(),
			},
			{
				Key: "action", Label: "On deny verdict", Input: pluginapi.InputSelect, Default: ActionBlock,
				Help: "Block rejects with an error and respond answers with an assistant reply, both carrying the server's deny_reason. Warn continues while recording the verdict: use it as a shadow mode to try a policy on live traffic.",
				Options: []pluginapi.Option{
					{Value: ActionBlock, Label: "Block"},
					{Value: ActionRespond, Label: "Respond"},
					{Value: ActionWarn, Label: "Warn (shadow mode)"},
				},
			},
			{
				Key: "message", Label: "Message", Input: pluginapi.InputText, Default: DefaultMessage,
				Help:        "Used when the deny verdict carries no deny_reason: the error message for block, the assistant reply for respond, and the request log note for warn.",
				Placeholder: DefaultMessage,
			},
			pluginapi.BlockStatusField(),
			{
				Key: "verdict_timeout_ms", Label: "Verdict timeout (ms)", Input: pluginapi.InputNumber, Default: DefaultVerdictTimeoutMS,
				Help:        "How long one verdict may take, connection included, from 1 to 60000. A slower verdict is a failure, and the guardrail's fail mode decides: closed (the default) rejects the request, open lets it through uninspected. The guardrail's own timeout_ms still applies when it is lower.",
				Placeholder: fmt.Sprint(DefaultVerdictTimeoutMS),
			},
		},
	}
}

// Init decodes and validates the instance configuration and prepares the
// client.
func (p *Plugin) Init(_ context.Context, raw json.RawMessage, host pluginapi.Host) error {
	s, err := decodeConfig(raw)
	if err != nil {
		return err
	}
	p.settings = s
	httpClient := http.DefaultClient
	if host != nil && host.HTTPClient() != nil {
		httpClient = host.HTTPClient()
	}
	p.client = newClient(httpClient, &p.settings)
	return nil
}

// Close releases nothing; the plugin holds no resources.
func (p *Plugin) Close(context.Context) error { return nil }

// Health sends the synthetic frame the protocol defines for connection
// tests (source.application "config-test", no request content). Any valid
// verdict, allow or deny, means the server is reachable and accepts the
// signature.
func (p *Plugin) Health(ctx context.Context) error {
	_, err := p.client.verdict(ctx, testFrame(p.tenantID))
	return err
}

// Summarize returns one line describing the instance for the dashboard
// list. It reads the action and the endpoint only: the stored config it is
// given may still hold the signing secret as an unresolved secret reference.
func (p *Plugin) Summarize(raw json.RawMessage) string {
	cfg, err := pluginapi.ParseConfig(Name, p.Manifest().ConfigSchema, raw)
	if err != nil {
		return ""
	}
	action := cfg.Choice("action", ActionBlock, ActionBlock, ActionRespond, ActionWarn)
	host, err := endpointHost(strings.TrimSpace(cfg.String("endpoint_url", "")))
	if err != nil || cfg.Err() != nil {
		return ""
	}
	return fmt.Sprintf("%s, %s", action, host)
}
