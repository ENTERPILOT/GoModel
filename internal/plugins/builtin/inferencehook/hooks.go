package inferencehook

import (
	"context"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Code marks a deny verdict in the request log.
const Code = "inference_hook_denied"

// OnPrompt sends the conversation as a prompt frame before the provider
// call.
func (p *Plugin) OnPrompt(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x.Prompt == nil {
		return pluginapi.Allow(), nil
	}
	f := p.newFrame(eventPrompt, x.Meta)
	if f.Messages = p.promptMessages(x.Prompt); len(f.Messages) == 0 {
		return pluginapi.Allow(), nil
	}
	v, err := p.client.verdict(ctx, f)
	if err != nil {
		return pluginapi.Decision{}, err
	}
	return p.decide(v), nil
}

// OnResponse sends one tool call frame per completion choice that calls
// tools, before the response reaches the client that would run them. As in
// the protocol, a response without tool calls is not sent. The first deny
// decides; the verdicts of the choices allowed before it stay in the detail
// as "allowed", so every frame sent can be found in the request log.
func (p *Plugin) OnResponse(ctx context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x.Response == nil {
		return pluginapi.Allow(), nil
	}
	decision := pluginapi.Allow()
	var allowed []any
	for _, choice := range x.Response.Choices {
		messages := toolCallMessage(choice)
		if messages == nil {
			continue
		}
		f := p.newFrame(eventToolCall, x.Meta)
		f.Messages = messages
		v, err := p.client.verdict(ctx, f)
		if err != nil {
			return pluginapi.Decision{}, err
		}
		if decision.Detail != nil {
			allowed = append(allowed, decision.Detail)
		}
		decision = p.decide(v)
		if v.action == verdictDeny {
			break
		}
	}
	if detail, ok := decision.Detail.(map[string]any); ok && len(allowed) > 0 {
		detail["allowed"] = allowed
	}
	return decision, nil
}

// StreamPolicy buffers the stream so the host runs OnResponse on the
// assembled completion before a tool call reaches the client.
func (p *Plugin) StreamPolicy() pluginapi.StreamPolicy {
	return pluginapi.StreamPolicy{Mode: pluginapi.StreamBuffer}
}

// OnStreamEvent passes every event; the verdict is taken in OnResponse.
func (p *Plugin) OnStreamEvent(context.Context, *pluginapi.Exchange, *pluginapi.StreamEvent) (pluginapi.StreamDecision, error) {
	return pluginapi.Pass(), nil
}

// OnStreamEnd allows; the verdict is taken in OnResponse.
func (p *Plugin) OnStreamEnd(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
	return pluginapi.Allow(), nil
}

// decide maps a verdict to the configured decision. The detail joins the
// request log entry to the security server's own record of the verdict.
func (p *Plugin) decide(v verdict) pluginapi.Decision {
	detail := map[string]any{"verdict": v.action, "hook_request_id": v.requestID}
	if v.referenceID != "" {
		detail["reference_id"] = v.referenceID
	}
	if v.action == verdictAllow {
		return pluginapi.Decision{Action: pluginapi.ActionAllow, Detail: detail}
	}
	enforcement := p.enforcement
	if v.denyReason != "" {
		detail["reason"] = v.denyReason
		enforcement.Message = v.denyReason
	}
	return enforcement.Enforce(Code, detail)
}
