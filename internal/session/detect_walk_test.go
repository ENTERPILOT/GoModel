package session

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/core"
)

// walkBodies exercise the single top-level walk against shapes where a naive
// first-member lookup would diverge from a gjson query: duplicated keys,
// escaped keys, nested values under duplicates and non-object roots.
var walkBodies = []string{
	`{"model":"gpt-4o","session_id":"s1","messages":[{"role":"user","content":"hi"}]}`,
	`{"session_id":"first","session_id":"second","messages":[{"role":"user","content":"hi"}]}`,
	`{"session_id":5,"session_id":"second","messages":[{"role":"user","content":"hi"}]}`,
	`{"conversation":"a","conversation":{"id":"b"},"input":"hi"}`,
	`{"conversation":{"x":1},"conversation":{"id":"b"},"input":"hi"}`,
	`{"conversation":{"id":"a"},"conversation":{"id":"b"},"input":"hi"}`,
	`{"metadata":5,"metadata":{"user_id":"user_x_session_87654321-4321-4321-4321-210987654321"},"messages":[]}`,
	`{"metadata":{"user_id":"{\"session_id\":\"12345678-1234-1234-1234-123456789012\"}"},"messages":[{"role":"user","content":"hi"}]}`,
	`{"session\u005fid":"escaped","messages":[{"role":"user","content":"hi"}]}`,
	`{"prompt_cache_key":"  padded  ","litellm_session_id":"","messages":[{"role":"user","content":"hi"}]}`,
	`{"model":"a","model":"b","messages":[{"role":"user","content":"hi"}],"messages":[{"role":"user","content":"other"}]}`,
	`{"input":[{"role":"system","content":"s"},{"role":"user","content":"u"}],"instructions":"be brief","tools":[{"b":1,"a":2}]}`,
	`{"system":[{"type":"text","text":"sys"}],"messages":[{"role":"assistant","content":"x"},{"role":"user","content":"y"}],"tools":[]}`,
	`{"messages":"plain string"}`,
	`{"tools":[{"type":"function"}],"tools":[{"type":"other"}],"messages":[{"role":"user","content":"hi"}]}`,
	`{"a":{"b":{"c":"deep"}},"a":{"b":{"c":"later"}},"messages":[{"role":"user","content":"hi"}]}`,
	`{"x-key":"dash","n":{"0":"zero"},"list":["l0","l1"],"messages":[{"role":"user","content":"hi"}]}`,
	`  {"session_id":"leading whitespace"}  `,
	`[{"session_id":"array root"}]`,
	`["l0","l1"]`,
	`"just a string"`,
	`42`,
	`null`,
	`{}`,
}

var walkPaths = []string{
	"metadata.user_id", "session_id", "litellm_session_id", "prompt_cache_key",
	"conversation", "conversation.id", "model", "tools.0.type", "a.b.c", "x-key",
	"n.0", "list.1", "0", "messages.#.role", "session\\_id", "a.b|@reverse",
}

func TestBodyRuleResultMatchesGJSON(t *testing.T) {
	rules := make([]Rule, 0, len(walkPaths))
	for _, path := range walkPaths {
		rules = append(rules, Rule{Source: SourceBody, BodyPath: path})
	}
	detector := NewDetector(rules, true)
	for _, body := range walkBodies {
		var storage [maxBodyFields]bodyMember
		members := storage[:len(detector.fields)]
		isObject := detector.collectMembers(body, members)
		for _, rule := range detector.bodyRules {
			want := gjson.Get(body, rule.BodyPath)
			got := bodyRuleResult(body, members, isObject, rule)
			assert.Equal(t, want.Type, got.Type, "body %s path %s", body, rule.BodyPath)
			assert.Equal(t, want.Raw, got.Raw, "body %s path %s", body, rule.BodyPath)
			assert.Equal(t, want.Str, got.Str, "body %s path %s", body, rule.BodyPath)
		}
	}
}

func TestCompileBodyRuleFallsBackBeyondFieldLimit(t *testing.T) {
	var rules []Rule
	for i := range maxBodyFields {
		rules = append(rules, Rule{Source: SourceBody, BodyPath: fmt.Sprintf("custom_%d", i)})
	}
	rules = append(rules, Rule{Source: SourceBody, BodyPath: "messages.#.role"})
	detector := NewDetector(rules, false)
	require.Len(t, detector.fields, maxBodyFields)

	last := detector.bodyRules[maxBodyFields-1]
	assert.Equal(t, -1, last.field, "rule past the field limit")
	assert.Equal(t, -1, detector.bodyRules[maxBodyFields].field, "non-plain path")

	body := fmt.Sprintf(`{"custom_%d":"tail-session","messages":[{"role":"user","content":"hi"}]}`, maxBodyFields-1)
	assert.Equal(t, "tail-session", detector.Detect(chatSnapshot(nil, body), ""))
}

// legacyDetect is the previous implementation: one gjson query per body rule
// and per anchor member. The single walk must produce identical ids.
func legacyDetect(rules []Rule, autoDetect bool, snapshot *core.RequestSnapshot, userPath string) string {
	body := snapshot.CapturedBodyView()
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	for _, rule := range rules {
		if rule.Source != SourceBody {
			continue
		}
		result := gjson.GetBytes(body, rule.BodyPath)
		if result.Type != gjson.String {
			continue
		}
		if id := cleanSessionID(applyTransform(strings.TrimSpace(result.Str), rule.Transform)); id != "" {
			return scopeSessionID(id, userPath)
		}
	}
	if !autoDetect {
		return ""
	}
	switch core.DescribeEndpoint(snapshot.Method, snapshot.Path).Operation {
	case core.OperationChatCompletions, core.OperationResponses:
	default:
		return ""
	}
	root := gjson.ParseBytes(body)
	anchor := contentAnchor{
		UserPath:     userPath,
		Model:        canonicalSegment(root.Get("model")),
		System:       canonicalSegment(root.Get("system")),
		Instructions: canonicalSegment(root.Get("instructions")),
		Tools:        canonicalSegment(root.Get("tools")),
	}
	messages := root.Get("messages")
	if !messages.Exists() {
		messages = root.Get("input")
	}
	if messages.Type == gjson.String {
		anchor.Opening = []json.RawMessage{canonicalSegment(messages)}
	} else {
		anchor.Opening = openingMessages(messages)
	}
	if len(anchor.Opening) == 0 {
		return ""
	}
	payload, err := json.Marshal(anchor)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return "auto-" + hex.EncodeToString(sum[:16])
}

func TestDetectMatchesPerRuleQueries(t *testing.T) {
	ruleSets := map[string][]Rule{
		"builtin":       BuiltinRules(),
		"custom":        {{Source: SourceBody, BodyPath: "a.b.c"}, {Source: SourceBody, BodyPath: "list.1"}},
		"no body rules": nil,
	}
	for name, rules := range ruleSets {
		for _, autoDetect := range []bool{false, true} {
			detector := NewDetector(rules, autoDetect)
			for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
				for _, userPath := range []string{"", "team/app"} {
					for _, body := range walkBodies {
						snapshot := core.NewRequestSnapshot(
							"POST", path, nil, nil, nil,
							"application/json", []byte(body), false, "req-1", nil,
						)
						want := legacyDetect(rules, autoDetect, snapshot, userPath)
						// Twice: the second call serves tools from the cache.
						for range 2 {
							got := detector.Detect(snapshot, userPath)
							assert.Equal(t, want, got, "rules %s auto %v path %s user %q body %s", name, autoDetect, path, userPath, body)
						}
					}
				}
			}
		}
	}
}

func TestDetectBodyRuleIDDoesNotAliasBody(t *testing.T) {
	body := []byte(`{"session_id":"original","messages":[]}`)
	snapshot := core.NewRequestSnapshotWithOwnedMaps(
		"POST", "/v1/chat/completions", nil, nil, nil,
		"application/json", body, false, "req-1", nil,
	)
	got := newBuiltinDetector(false).Detect(snapshot, "")
	require.Equal(t, "original", got)

	copy(body[bytes.Index(body, []byte("original")):], "mutated!")
	assert.Equal(t, "original", got)
}
