package presidio

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
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
)

var roleOptions = []pluginapi.Option{
	{Value: "system", Label: "System (and developer)"},
	{Value: "user", Label: "User"},
	{Value: "assistant", Label: "Assistant"},
	{Value: "tool", Label: "Tool results"},
}

// config is the instance configuration as stored by the host. Values are
// kept raw so each key can be decoded with a message naming the key.
type config struct {
	AnalyzerURL      json.RawMessage `json:"analyzer_url"`
	APIKey           json.RawMessage `json:"api_key"`
	Language         json.RawMessage `json:"language"`
	Entities         json.RawMessage `json:"entities"`
	BlockEntities    json.RawMessage `json:"block_entities"`
	ScoreThreshold   json.RawMessage `json:"score_threshold"`
	AllowList        json.RawMessage `json:"allow_list"`
	AdHocRecognizers json.RawMessage `json:"ad_hoc_recognizers"`
	Roles            json.RawMessage `json:"roles"`
	Action           json.RawMessage `json:"action"`
	Operator         json.RawMessage `json:"operator"`
	Restore          json.RawMessage `json:"restore"`
	Message          json.RawMessage `json:"message"`
	BlockStatus      json.RawMessage `json:"block_status"`
	StreamChunk      json.RawMessage `json:"stream_chunk"`
	StreamLookbehind json.RawMessage `json:"stream_lookbehind"`
}

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
	restore          bool
	message          string
	blockStatus      int
	streamChunk      int
	lookbehind       int
}

func decodeConfig(raw json.RawMessage) (settings, error) {
	var cfg config
	if len(strings.TrimSpace(string(raw))) > 0 && string(raw) != "null" {
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return settings{}, fmt.Errorf("%s: invalid config: %w", Name, err)
		}
	}
	s := settings{
		analyzerURL: DefaultAnalyzerURL,
		language:    DefaultLanguage,
		roles:       map[pluginapi.Role]bool{pluginapi.RoleUser: true, pluginapi.RoleAssistant: true, pluginapi.RoleTool: true},
		action:      ActionAnonymize,
		operator:    OperatorReplace,
		message:     DefaultMessage,
		streamChunk: DefaultStreamChunk,
		lookbehind:  DefaultStreamLookbehind,
	}
	var err error
	if s.analyzerURL, err = parseString("analyzer_url", cfg.AnalyzerURL, s.analyzerURL); err != nil {
		return settings{}, err
	}
	s.analyzerURL = strings.TrimRight(strings.TrimSpace(s.analyzerURL), "/")
	if s.analyzerURL == "" {
		s.analyzerURL = DefaultAnalyzerURL
	}
	if !strings.HasPrefix(s.analyzerURL, "http://") && !strings.HasPrefix(s.analyzerURL, "https://") {
		return settings{}, fmt.Errorf("%s: analyzer_url must start with http:// or https://", Name)
	}
	if s.apiKey, err = parseString("api_key", cfg.APIKey, ""); err != nil {
		return settings{}, err
	}
	s.apiKey = strings.TrimSpace(s.apiKey)
	if s.apiKey != "" && !strings.HasPrefix(s.analyzerURL, "https://") && !loopbackURL(s.analyzerURL) {
		return settings{}, fmt.Errorf("%s: api_key needs an https:// analyzer_url (plain http is only allowed for localhost), so the token is not sent in clear", Name)
	}
	if s.language, err = parseString("language", cfg.Language, s.language); err != nil {
		return settings{}, err
	}
	if s.language = strings.TrimSpace(s.language); s.language == "" {
		s.language = DefaultLanguage
	}
	if s.entities, err = parseEntities("entities", cfg.Entities); err != nil {
		return settings{}, err
	}
	blocked, err := parseEntities("block_entities", cfg.BlockEntities)
	if err != nil {
		return settings{}, err
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
	if s.scoreThreshold, err = parseOptionalFloat("score_threshold", cfg.ScoreThreshold, 0, 1); err != nil {
		return settings{}, err
	}
	if s.allowList, err = parseList("allow_list", cfg.AllowList); err != nil {
		return settings{}, err
	}
	if s.adHocRecognizers, err = parseJSONArray("ad_hoc_recognizers", cfg.AdHocRecognizers); err != nil {
		return settings{}, err
	}
	roles, err := parseList("roles", cfg.Roles)
	if err != nil {
		return settings{}, err
	}
	if roles != nil {
		s.roles = map[pluginapi.Role]bool{}
		for _, r := range roles {
			if !validRole(r) {
				return settings{}, fmt.Errorf("%s: roles: unknown role %q (use system, user, assistant, tool)", Name, r)
			}
			s.roles[pluginapi.Role(r)] = true
			if r == "system" {
				s.roles[pluginapi.RoleDeveloper] = true
			}
		}
	}
	if s.action, err = parseChoice("action", cfg.Action, s.action, ActionAnonymize, ActionBlock, ActionRespond, ActionWarn); err != nil {
		return settings{}, err
	}
	if s.operator, err = parseChoice("operator", cfg.Operator, s.operator, OperatorReplace, OperatorMask, OperatorRedact, OperatorHash); err != nil {
		return settings{}, err
	}
	if s.restore, err = parseBool("restore", cfg.Restore); err != nil {
		return settings{}, err
	}
	if s.restore && s.operator != OperatorReplace {
		return settings{}, fmt.Errorf("%s: restore needs operator replace (placeholders are what gets restored), got %s", Name, s.operator)
	}
	if s.message, err = parseString("message", cfg.Message, s.message); err != nil {
		return settings{}, err
	}
	if s.blockStatus, err = parseInt("block_status", cfg.BlockStatus, 0, 0, 599); err != nil {
		return settings{}, err
	}
	if s.blockStatus != 0 && s.blockStatus < 400 {
		return settings{}, fmt.Errorf("%s: block_status must be an HTTP status between 400 and 599, got %d", Name, s.blockStatus)
	}
	if s.streamChunk, err = parseInt("stream_chunk", cfg.StreamChunk, s.streamChunk, 0, 16384); err != nil {
		return settings{}, err
	}
	if s.lookbehind, err = parseInt("stream_lookbehind", cfg.StreamLookbehind, s.lookbehind, 0, 1<<20); err != nil {
		return settings{}, err
	}
	return s, nil
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

func validRole(r string) bool {
	for _, o := range roleOptions {
		if o.Value == r {
			return true
		}
	}
	return false
}

func isUnset(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// parseString decodes a JSON string; absent or null keeps def.
func parseString(key string, raw json.RawMessage, def string) (string, error) {
	if isUnset(raw) {
		return def, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s: %s must be a string", Name, key)
	}
	return s, nil
}

// parseChoice decodes a select value; absent, null, or "" keeps def.
func parseChoice(key string, raw json.RawMessage, def string, allowed ...string) (string, error) {
	s, err := parseString(key, raw, def)
	if err != nil {
		return "", err
	}
	if s == "" {
		return def, nil
	}
	if slices.Contains(allowed, s) {
		return s, nil
	}
	return "", fmt.Errorf("%s: %s must be one of %s; got %q", Name, key, strings.Join(allowed, ", "), s)
}

// parseBool accepts a JSON bool or the strings true/false/yes/no; absent,
// null, or "" is false.
func parseBool(key string, raw json.RawMessage) (bool, error) {
	if isUnset(raw) {
		return false, nil
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "yes", "1", "on":
			return true, nil
		case "false", "no", "0", "off", "":
			return false, nil
		}
	}
	return false, fmt.Errorf("%s: %s must be true or false, got %s", Name, key, raw)
}

// parseNumber accepts a JSON number or a numeric string. ok is false when
// the value is absent, null, or "".
func parseNumber(key string, raw json.RawMessage) (f float64, ok bool, err error) {
	if isUnset(raw) {
		return 0, false, nil
	}
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, true, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, false, fmt.Errorf("%s: %s must be a number, got %s", Name, key, raw)
	}
	if strings.TrimSpace(s) == "" {
		return 0, false, nil
	}
	if f, err = strconv.ParseFloat(strings.TrimSpace(s), 64); err != nil {
		return 0, false, fmt.Errorf("%s: %s must be a number, got %q", Name, key, s)
	}
	return f, true, nil
}

// parseInt accepts a whole number within [lo, hi]; absent keeps def.
func parseInt(key string, raw json.RawMessage, def, lo, hi int) (int, error) {
	f, ok, err := parseNumber(key, raw)
	if err != nil || !ok {
		return def, err
	}
	n := int(f)
	if float64(n) != f || n < lo || n > hi {
		return 0, fmt.Errorf("%s: %s must be a whole number between %d and %d, got %v", Name, key, lo, hi, f)
	}
	return n, nil
}

// parseOptionalFloat accepts a number within [lo, hi]; absent returns nil.
func parseOptionalFloat(key string, raw json.RawMessage, lo, hi float64) (*float64, error) {
	f, ok, err := parseNumber(key, raw)
	if err != nil || !ok {
		return nil, err
	}
	if f < lo || f > hi {
		return nil, fmt.Errorf("%s: %s must be between %v and %v, got %v", Name, key, lo, hi, f)
	}
	return &f, nil
}

// parseList decodes a list value: a JSON array of strings or one string
// split on commas and newlines. Absent or null returns nil; an empty list
// is returned as an empty (non-nil) slice. Items are trimmed and blanks
// dropped.
func parseList(key string, raw json.RawMessage) ([]string, error) {
	if isUnset(raw) {
		return nil, nil
	}
	var items []string
	if err := json.Unmarshal(raw, &items); err != nil {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, fmt.Errorf("%s: %s must be a list of strings", Name, key)
		}
		items = strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '\n' })
	}
	out := []string{}
	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out, nil
}

// parseEntities decodes a list of entity types, upper-cased the way the
// analyzer names them. An empty list is returned as nil.
func parseEntities(key string, raw json.RawMessage) ([]string, error) {
	items, err := parseList(key, raw)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, item := range items {
		item = strings.ToUpper(item)
		if !slices.Contains(out, item) {
			out = append(out, item)
		}
	}
	return out, nil
}

// parseJSONArray decodes a textarea holding a JSON array: either the array
// itself or a string containing one. Absent, null, or blank returns nil.
func parseJSONArray(key string, raw json.RawMessage) (json.RawMessage, error) {
	if isUnset(raw) {
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
