// Package headeredit is the built-in header_edit plugin: it sets, adds, and
// removes HTTP headers on the request, the client response, and the upstream
// provider call. It doubles as a reference implementation of a non-mutating
// pluginapi plugin that uses Exchange.Headers and Exchange.Values.
package headeredit

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
const Name = "header_edit"

var instances atomic.Int64

// Plugin is one configured header_edit instance.
type Plugin struct {
	key      string // Exchange.Values key prefix, unique per instance
	request  []edit
	response []edit
	upstream []edit
}

// New returns an unconfigured plugin; call Init before use.
func New() pluginapi.Plugin { return &Plugin{} }

// Manifest describes the plugin and its configuration form.
func (p *Plugin) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{
		Name:        Name,
		Version:     "1.0.0",
		Description: "Sets, adds, and removes HTTP headers on the request, the client response, and the upstream call.",
		Kinds:       []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse},
		Mutates:     false,
		ConfigSchema: []pluginapi.Field{
			{
				Key: "request_set", Label: "Request headers to set", Input: pluginapi.InputTextarea, Default: "",
				Help:        "One \"Name: value\" per line. Replaces the inbound request header on the live request after the prompt phase, as seen by later plugins and request logging; not forwarded to the provider. Blank lines and lines starting with # are ignored.",
				Placeholder: "X-Team: platform\n# X-Env: prod",
			},
			{
				Key: "request_remove", Label: "Request headers to remove", Input: pluginapi.InputTextarea, Default: "",
				Help:        "One header name per line. Drops the header from the live request after the prompt phase, as seen by later plugins and request logging.",
				Placeholder: "X-Debug",
			},
			{
				Key: "response_set", Label: "Response headers to set", Input: pluginapi.InputTextarea, Default: "",
				Help:        "One \"Name: value\" per line. Replaces the header on the response sent to the client.",
				Placeholder: "Cache-Control: no-store",
			},
			{
				Key: "response_add", Label: "Response headers to add", Input: pluginapi.InputTextarea, Default: "",
				Help:        "One \"Name: value\" per line. Appends a value to the client response header, keeping existing values. Applied once per request even when the instance runs in several phases.",
				Placeholder: "X-Served-By: gomodel",
			},
			{
				Key: "response_remove", Label: "Response headers to remove", Input: pluginapi.InputTextarea, Default: "",
				Help:        "One header name per line. Drops the header from the response sent to the client.",
				Placeholder: "X-Request-Id",
			},
			{
				Key: "upstream_set", Label: "Upstream headers to set", Input: pluginapi.InputTextarea, Default: "",
				Help:        "One \"Name: value\" per line. Adds static headers to the provider call. Host support is limited in this version: providers that do not accept extra headers ignore the field.",
				Placeholder: "X-Tenant: acme",
			},
		},
	}
}

// Init decodes and validates the instance configuration. Unknown keys and
// credential header names are rejected.
func (p *Plugin) Init(_ context.Context, raw json.RawMessage, _ pluginapi.Host) error {
	cfg, err := decodeConfig(raw)
	if err != nil {
		return err
	}
	request, response, upstream, err := compile(cfg)
	if err != nil {
		return err
	}
	p.request, p.response, p.upstream = request, response, upstream
	p.key = fmt.Sprintf("%s:%d", Name, instances.Add(1))
	return nil
}

// Close releases nothing; the plugin holds no resources.
func (p *Plugin) Close(context.Context) error { return nil }

// OnPrompt applies the configured edits before the provider call.
func (p *Plugin) OnPrompt(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	return p.apply(x), nil
}

// OnResponse applies the configured edits before the response reaches the
// client. Edits already applied in the prompt phase are not added twice.
func (p *Plugin) OnResponse(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	return p.apply(x), nil
}

// Summarize returns one line describing the configured edits for the
// dashboard list.
func (p *Plugin) Summarize(raw json.RawMessage) string {
	cfg, err := decodeConfig(raw)
	if err != nil {
		return ""
	}
	request, response, upstream, err := compile(cfg)
	if err != nil {
		return ""
	}
	var parts []string
	for _, group := range []struct {
		name  string
		edits []edit
	}{{"request", request}, {"response", response}, {"upstream", upstream}} {
		if s := summarizeEdits(group.edits); s != "" {
			parts = append(parts, group.name+": "+s)
		}
	}
	if len(parts) == 0 {
		return "no header edits"
	}
	return strings.Join(parts, "; ")
}

func summarizeEdits(edits []edit) string {
	byOp := map[op][]string{}
	for _, e := range edits {
		byOp[e.op] = append(byOp[e.op], e.name)
	}
	var parts []string
	for _, o := range []op{opSet, opAdd, opRemove} {
		if names := byOp[o]; len(names) > 0 {
			parts = append(parts, string(o)+" "+strings.Join(names, ", "))
		}
	}
	return strings.Join(parts, ", ")
}

// compile parses every config field. Within a header set the order is set,
// then add, then remove, so a name listed in both set and remove ends up
// removed.
func compile(cfg config) (request, response, upstream []edit, err error) {
	groups := []struct {
		field   string
		entries json.RawMessage
		o       op
		dst     *[]edit
	}{
		{"request_set", cfg.RequestSet, opSet, &request},
		{"request_remove", cfg.RequestRemove, opRemove, &request},
		{"response_set", cfg.ResponseSet, opSet, &response},
		{"response_add", cfg.ResponseAdd, opAdd, &response},
		{"response_remove", cfg.ResponseRemove, opRemove, &response},
		{"upstream_set", cfg.UpstreamSet, opSet, &upstream},
	}
	for _, g := range groups {
		edits, err := parseEdits(g.field, g.entries, g.o)
		if err != nil {
			return nil, nil, nil, err
		}
		*g.dst = append(*g.dst, edits...)
	}
	return request, response, upstream, nil
}

// apply runs the edits on the exchange headers. Set and remove are
// idempotent and run in every phase; add runs once per request, tracked in
// Exchange.Values.
func (p *Plugin) apply(x *pluginapi.Exchange) pluginapi.Decision {
	if x.Headers == nil {
		x.Headers = &pluginapi.Headers{}
	}
	addKey := p.key + ":added"
	_, added := x.Values.Get(addKey)

	detail := map[string][]string{}
	if names := applyEdits(&x.Headers.Request, p.request, true, false); len(names) > 0 {
		detail["request"] = names
	}
	// Response headers do not exist yet when the plugin runs, so a removal is
	// recorded as an empty value, which the gateway applies as a deletion.
	if names := applyEdits(&x.Headers.Response, p.response, !added, true); len(names) > 0 {
		detail["response"] = names
	}
	if names := applyEdits(&x.Headers.Upstream, p.upstream, true, false); len(names) > 0 {
		detail["upstream"] = names
	}
	if !added && x.Values != nil {
		x.Values.Set(addKey, true)
	}
	return pluginapi.Decision{Action: pluginapi.ActionAllow, Detail: detail}
}

// applyEdits applies edits to h (allocating it when nil) and returns the
// canonical names of the headers it changed, in order, without duplicates.
// With markRemovals a remove sets the header to the empty string (the
// pluginapi convention for removing a response header) instead of deleting
// it from h.
func applyEdits(h *http.Header, edits []edit, allowAdd, markRemovals bool) []string {
	if len(edits) == 0 {
		return nil
	}
	if *h == nil {
		*h = http.Header{}
	}
	var names []string
	seen := map[string]bool{}
	for _, e := range edits {
		switch e.op {
		case opSet:
			h.Set(e.name, e.value)
		case opAdd:
			if !allowAdd {
				continue
			}
			h.Add(e.name, e.value)
		case opRemove:
			if markRemovals {
				h.Set(e.name, "")
				break
			}
			if _, ok := (*h)[e.name]; !ok {
				continue
			}
			h.Del(e.name)
		}
		if !seen[e.name] {
			seen[e.name] = true
			names = append(names, e.name)
		}
	}
	return names
}
