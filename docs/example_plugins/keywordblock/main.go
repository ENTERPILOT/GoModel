// keyword_block is an example GoModel plugin built as a shared object. It
// blocks, answers, or flags a request when the last user message contains a
// configured keyword, and does the same for the assistant's reply.
//
// Build (from the GoModel checkout, with the gomodel binary that will load it):
//
//	go run ./cmd/gomodel plugin build ./examples/plugins/keywordblock -o plugins/keyword_block.so
//	go run ./cmd/gomodel plugin inspect plugins/keyword_block.so
//
// Load (config.yaml, or PLUGINS_SEARCH_PATHS=./plugins):
//
//	plugins:
//	  search_paths: ["./plugins"]
//	  load:
//	    - file: keyword_block.so
//	      sha256: ""   # optional pin; `shasum -a 256 plugins/keyword_block.so`
//
// The plugin type then appears as "keyword_block" wherever plugin instances
// are configured (the dashboard, plugins.instances, workflows).
//
// Exact-toolchain constraint: Go's plugin package refuses a shared object
// unless it was built with the same Go version, the same build flags
// (-trimpath, -race, -tags), and identical sources of every package shared
// with the host (the standard library and pluginapi). `gomodel plugin build`
// copies the flags of the gomodel binary that runs it, so build plugins with
// the binary that loads them and rebuild after every GoModel or Go upgrade.
// Loading needs a cgo-enabled binary on Linux, macOS, or FreeBSD (`make
// build-plugins`, or the gomodel:<version>-plugins image); the default static
// binary reports a clear error instead.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/enterpilot/gomodel/pluginapi"
)

// GoModelPlugin is the constructor GoModel looks up. It returns a fresh
// instance per call, so one shared object can back several configured
// instances with different settings.
func GoModelPlugin() pluginapi.Plugin {
	return &keywordBlock{}
}

const (
	pluginName = "keyword_block"

	actionBlock   = "block"
	actionRespond = "respond"
	actionWarn    = "warn"

	defaultMessage = "This request was stopped by the keyword policy."
)

// keywordBlock is one configured instance.
type keywordBlock struct {
	keywords []string
	action   string
	message  string
	log      *slog.Logger
}

// settings is the instance config as validated against the manifest schema.
// Keywords arrives as the textarea's text (one per line); a JSON array is
// accepted too so hand-written YAML config can use a list.
type settings struct {
	Keywords lines  `json:"keywords"`
	Action   string `json:"action"`
	Message  string `json:"message"`
}

type lines []string

func (l *lines) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*l = splitLines(text)
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return errors.New("keywords must be a string (one keyword per line) or a list of strings")
	}
	*l = splitLines(strings.Join(list, "\n"))
	return nil
}

func splitLines(text string) []string {
	var out []string
	for line := range strings.SplitSeq(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, strings.ToLower(line))
		}
	}
	return out
}

func (k *keywordBlock) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{
		Name:        pluginName,
		Version:     "1.0.0",
		Description: "Blocks, answers, or flags requests and responses containing configured keywords.",
		Kinds:       []pluginapi.Kind{pluginapi.KindPrompt, pluginapi.KindResponse},
		ConfigSchema: []pluginapi.Field{
			{
				Key:      "keywords",
				Label:    "Keywords",
				Input:    pluginapi.InputTextarea,
				Required: true,
				Help:     "One keyword or phrase per line. Matching is case-insensitive.",
			},
			{
				Key:     "action",
				Label:   "Action",
				Input:   pluginapi.InputSelect,
				Default: actionBlock,
				Help:    "block rejects with an error, respond answers with the message instead of calling the model, warn lets the request through and records the match.",
				Options: []pluginapi.Option{
					{Value: actionBlock, Label: "Block"},
					{Value: actionRespond, Label: "Respond with message"},
					{Value: actionWarn, Label: "Warn only"},
				},
			},
			{
				Key:     "message",
				Label:   "Message",
				Input:   pluginapi.InputText,
				Default: defaultMessage,
				Help:    "Sent to the client on block or respond; recorded on warn.",
			},
		},
	}
}

func (k *keywordBlock) Init(_ context.Context, config json.RawMessage, host pluginapi.Host) error {
	var s settings
	if len(config) > 0 {
		if err := json.Unmarshal(config, &s); err != nil {
			return fmt.Errorf("%s: invalid config: %w", pluginName, err)
		}
	}
	if len(s.Keywords) == 0 {
		return fmt.Errorf("%s: keywords must list at least one keyword", pluginName)
	}
	switch s.Action {
	case "":
		s.Action = actionBlock
	case actionBlock, actionRespond, actionWarn:
	default:
		return fmt.Errorf("%s: action %q must be block, respond, or warn", pluginName, s.Action)
	}
	if s.Message == "" {
		s.Message = defaultMessage
	}
	k.keywords, k.action, k.message = s.Keywords, s.Action, s.Message
	k.log = host.Logger()
	return nil
}

func (k *keywordBlock) Close(context.Context) error { return nil }

// OnPrompt checks the last user message before the provider is called.
func (k *keywordBlock) OnPrompt(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x.Prompt == nil {
		return pluginapi.Allow(), nil
	}
	last := x.Prompt.LastUser()
	if last == nil {
		return pluginapi.Allow(), nil
	}
	return k.decide("prompt", last.Text()), nil
}

// OnResponse checks every assistant choice of a complete response.
func (k *keywordBlock) OnResponse(_ context.Context, x *pluginapi.Exchange) (pluginapi.Decision, error) {
	if x.Response == nil {
		return pluginapi.Allow(), nil
	}
	for i := range x.Response.Choices {
		if d := k.decide("response", x.Response.Text(i)); d.Action != pluginapi.ActionAllow {
			return d, nil
		}
	}
	return pluginapi.Allow(), nil
}

// decide returns the configured decision when text contains a keyword. Only
// the keyword is recorded, never the text itself.
func (k *keywordBlock) decide(phase, text string) pluginapi.Decision {
	keyword, found := k.match(text)
	if !found {
		return pluginapi.Allow()
	}
	if k.log != nil {
		k.log.Info("keyword matched", "phase", phase, "keyword", keyword, "action", k.action)
	}
	detail := map[string]any{"phase": phase, "keyword": keyword}
	switch k.action {
	case actionRespond:
		d := pluginapi.Respond(k.message)
		d.Code, d.Detail = pluginName, detail
		return d
	case actionWarn:
		return pluginapi.Warn(pluginName, k.message, detail)
	default:
		d := pluginapi.Block(0, pluginName, k.message)
		d.Detail = detail
		return d
	}
}

func (k *keywordBlock) match(text string) (string, bool) {
	lower := strings.ToLower(text)
	for _, kw := range k.keywords {
		if strings.Contains(lower, kw) {
			return kw, true
		}
	}
	return "", false
}

// main is required for -buildmode=plugin (package main) and never runs.
func main() {}
