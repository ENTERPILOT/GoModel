package stringreplace

import (
	"encoding/json"
	"fmt"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Mode values for the "mode" config key.
const (
	ModeLiteral = "literal"
	ModeRegex   = "regex"
)

// OnMatch values for the "on_match" config key.
const (
	OnMatchReplace = "replace"
	OnMatchBlock   = "block"
	OnMatchRespond = "respond"
	OnMatchWarn    = "warn"
)

// Defaults for optional config keys.
const (
	DefaultMessage          = "Request blocked by policy"
	DefaultStreamLookbehind = 64
)

// enforcementMessage resolves the configured message. A blank one — unset, or
// cleared in the dashboard — falls back to the block-phrased default for the
// outcomes that stop the request, so a block never answers with an empty
// error; warn keeps no note, since its message is only an audit note and the
// default would read as a block in the dashboard.
func enforcementMessage(configured, onMatch string) string {
	if configured != "" || onMatch == OnMatchWarn {
		return configured
	}
	return DefaultMessage
}

// settings is the validated configuration.
type settings struct {
	rules           []rule
	mode            string
	caseInsensitive bool
	roles           map[pluginapi.Role]bool
	onMatch         string
	enforcement     pluginapi.Enforcement
	lookbehind      int
}

func decodeConfig(raw json.RawMessage) (settings, error) {
	cfg, err := pluginapi.ParseConfig(Name, New().Manifest().ConfigSchema, raw)
	if err != nil {
		return settings{}, err
	}
	s := settings{
		mode:            cfg.Choice("mode", ModeLiteral, ModeLiteral, ModeRegex),
		caseInsensitive: cfg.Bool("case_insensitive"),
		roles:           cfg.Roles("roles", pluginapi.RoleUser),
		onMatch:         cfg.Choice("on_match", OnMatchReplace, OnMatchReplace, OnMatchBlock, OnMatchRespond, OnMatchWarn),
		lookbehind:      cfg.Int("stream_lookbehind", DefaultStreamLookbehind, 0, 1<<20),
	}
	s.enforcement = pluginapi.Enforcement{
		Action:      pluginapi.Action(s.onMatch),
		Message:     enforcementMessage(cfg.String("message", ""), s.onMatch),
		BlockStatus: cfg.BlockStatus("block_status"),
	}
	ruleLines := cfg.Lines("rules")
	if err := cfg.Err(); err != nil {
		return settings{}, err
	}
	s.rules, err = parseRules(ruleLines, s.mode, s.caseInsensitive)
	if err != nil {
		return settings{}, err
	}
	if len(s.rules) == 0 {
		return settings{}, fmt.Errorf("%s: rules is required: add at least one \"find => replace\" line", Name)
	}
	return s, nil
}
