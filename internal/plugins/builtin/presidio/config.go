package presidio

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Action values for the "action" config key: what happens when an entity
// is found.
const (
	ActionAnonymize = "anonymize"
	ActionBlock     = "block"
	ActionRespond   = "respond"
	ActionWarn      = "warn"
)

// Defaults for optional config keys.
const (
	DefaultAnalyzerURL      = "http://localhost:5002"
	DefaultLanguage         = "en"
	DefaultMessage          = "Request blocked: it contains personal data"
	DefaultStreamChunk      = 256
	DefaultStreamLookbehind = 64
	// MaxStreamLookbehind bounds stream_lookbehind: far longer than any
	// value or placeholder, short enough that the client is not kept
	// waiting on held-back text.
	MaxStreamLookbehind = 4096
)

// settings is the validated configuration.
type settings struct {
	analyzerURL      string
	apiKey           string
	language         string
	entities         []string
	blockEntities    map[string]bool
	scoreThreshold   *float64
	allowList        []string
	adHocRecognizers json.RawMessage
	roles            map[pluginapi.Role]bool
	action           string
	operator         string
	placeholders     *placeholderFormat
	restore          bool
	// restoreRoles are the prompt roles whose values come back; the default
	// list unless restoreRolesSet. See [roleFilter].
	restoreRoles    map[pluginapi.Role]bool
	restoreRolesSet bool
	// restoreTools, when set, are the only tools whose call arguments get
	// values back; keepTools never get them.
	restoreTools map[string]bool
	keepTools    map[string]bool
	enforcement  pluginapi.Enforcement
	streamChunk  int
	lookbehind   int
}

func decodeConfig(raw json.RawMessage) (settings, error) {
	cfg, err := pluginapi.ParseConfig(Name, New().Manifest().ConfigSchema, raw)
	if err != nil {
		return settings{}, err
	}
	s := settings{
		analyzerURL:    strings.TrimRight(strings.TrimSpace(cfg.String("analyzer_url", DefaultAnalyzerURL)), "/"),
		apiKey:         strings.TrimSpace(cfg.String("api_key", "")),
		language:       strings.TrimSpace(cfg.String("language", DefaultLanguage)),
		entities:       entityTypes(cfg.List("entities")),
		scoreThreshold: cfg.OptionalFloat("score_threshold", 0, 1),
		allowList:      cfg.List("allow_list"),
		roles:          cfg.Roles("roles", pluginapi.RoleUser, pluginapi.RoleAssistant, pluginapi.RoleTool),
		action:         cfg.Choice("action", ActionAnonymize, ActionAnonymize, ActionBlock, ActionRespond, ActionWarn),
		operator:       cfg.Choice("operator", OperatorReplace, OperatorReplace, OperatorMask, OperatorRedact, OperatorHash),
		restore:        cfg.Bool("restore"),
		restoreRoles:   cfg.Roles("restore_roles", pluginapi.RoleUser, pluginapi.RoleAssistant, pluginapi.RoleTool),
		restoreTools:   nameSet(cfg.List("restore_tools")),
		keepTools:      nameSet(cfg.List("restore_tools_exclude")),
		streamChunk:    cfg.Int("stream_chunk", DefaultStreamChunk, 0, 16384),
		lookbehind:     cfg.Int("stream_lookbehind", DefaultStreamLookbehind, 0, MaxStreamLookbehind),
	}
	s.restoreRolesSet = cfg.List("restore_roles") != nil
	s.enforcement = pluginapi.Enforcement{
		Action:      pluginapi.Action(s.action),
		Message:     cfg.String("message", DefaultMessage),
		BlockStatus: cfg.BlockStatus("block_status"),
	}
	blocked := entityTypes(cfg.List("block_entities"))
	format := strings.TrimSpace(cfg.String("placeholder_format", DefaultPlaceholderFormat))
	if format == "" {
		format = DefaultPlaceholderFormat
	}
	if s.adHocRecognizers, err = parseJSONArray("ad_hoc_recognizers", cfg.Raw("ad_hoc_recognizers")); err != nil {
		return settings{}, err
	}
	if err := cfg.Err(); err != nil {
		return settings{}, err
	}
	if s.placeholders, err = parsePlaceholderFormat(format); err != nil {
		return settings{}, fmt.Errorf("%s: placeholder_format %w", Name, err)
	}
	if s.analyzerURL == "" {
		s.analyzerURL = DefaultAnalyzerURL
	}
	if !strings.HasPrefix(s.analyzerURL, "http://") && !strings.HasPrefix(s.analyzerURL, "https://") {
		return settings{}, fmt.Errorf("%s: analyzer_url must start with http:// or https://", Name)
	}
	if s.apiKey != "" && !strings.HasPrefix(s.analyzerURL, "https://") && !loopbackURL(s.analyzerURL) {
		return settings{}, fmt.Errorf("%s: api_key needs an https:// analyzer_url (plain http is only allowed for localhost), so the token is not sent in clear", Name)
	}
	if s.language == "" {
		s.language = DefaultLanguage
	}
	if len(blocked) > 0 {
		s.blockEntities = map[string]bool{}
		for _, e := range blocked {
			s.blockEntities[e] = true
			if len(s.entities) > 0 && !slices.Contains(s.entities, e) {
				s.entities = append(s.entities, e)
			}
		}
	}
	if s.restore && s.operator != OperatorReplace {
		return settings{}, fmt.Errorf("%s: restore needs operator replace (placeholders are what gets restored), got %s", Name, s.operator)
	}
	return s, nil
}

// entityTypes upper-cases and de-duplicates entity type names the way the
// analyzer spells them. An empty list is nil.
func entityTypes(items []string) []string {
	var out []string
	for _, item := range items {
		item = strings.ToUpper(item)
		if !slices.Contains(out, item) {
			out = append(out, item)
		}
	}
	return out
}

// nameSet turns a list of tool names into a set; an empty list is nil.
func nameSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item] = true
	}
	return set
}

// restoresTool reports whether the arguments of a call to the named tool
// get restored values: when restore_tools is empty or names it, and
// restore_tools_exclude does not. A call whose name is unknown only gets
// them when restore_tools is empty.
func (s *settings) restoresTool(name string) bool {
	if s.keepTools[name] {
		return false
	}
	return s.restoreTools == nil || s.restoreTools[name]
}

// loopbackURL reports whether the URL points at this host.
func loopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// parseJSONArray decodes a textarea holding a JSON array: either the array
// itself or a string containing one. Absent, null, or blank returns nil.
func parseJSONArray(key string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		raw = json.RawMessage(text)
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("%s: %s must be a JSON array", Name, key)
	}
	if len(items) == 0 {
		return nil, nil
	}
	compact, err := json.Marshal(items)
	if err != nil {
		return nil, fmt.Errorf("%s: %s: %w", Name, key, err)
	}
	return compact, nil
}
