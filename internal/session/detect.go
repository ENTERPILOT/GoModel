package session

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/core"
)

// Detector resolves the session id for a request. Explicit header rules win
// over body rules, which win over content-based auto-detection.
type Detector struct {
	headerRules []Rule
	bodyRules   []Rule
	autoDetect  bool
}

// NewDetector builds a Detector from an ordered rule list. Rules keep their
// relative order within each source; header rules are always evaluated first.
func NewDetector(rules []Rule, autoDetect bool) *Detector {
	d := &Detector{autoDetect: autoDetect}
	for _, rule := range rules {
		switch rule.Source {
		case SourceHeader:
			if rule.Header != "" {
				d.headerRules = append(d.headerRules, rule)
			}
		case SourceBody:
			if rule.BodyPath != "" {
				d.bodyRules = append(d.bodyRules, rule)
			}
		}
	}
	return d
}

// Detect returns the stable session id for the captured request, or "" when
// the request carries no session signal. Explicit client-provided ids are
// scoped by user path so no caller-controlled value can collide across
// tenants.
func (d *Detector) Detect(snapshot *core.RequestSnapshot, userPath string) string {
	if d == nil || snapshot == nil {
		return ""
	}
	if id := d.detectFromHeaders(snapshot); id != "" {
		return scopeSessionID(id, userPath)
	}
	body := snapshot.CapturedBodyView()
	if snapshot.BodyNotCaptured || len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	if id := d.detectFromBody(body); id != "" {
		return scopeSessionID(id, userPath)
	}
	if d.autoDetect {
		return contentSessionID(snapshot, body, userPath)
	}
	return ""
}

func (d *Detector) detectFromHeaders(snapshot *core.RequestSnapshot) string {
	headers := snapshot.HeadersView()
	if len(headers) == 0 {
		return ""
	}
	for _, rule := range d.headerRules {
		for key, values := range headers {
			if !strings.EqualFold(key, rule.Header) {
				continue
			}
			for _, value := range values {
				if id := cleanSessionID(applyTransform(strings.TrimSpace(value), rule.Transform)); id != "" {
					return id
				}
			}
		}
	}
	return ""
}

func (d *Detector) detectFromBody(body []byte) string {
	for _, rule := range d.bodyRules {
		result := gjson.GetBytes(body, rule.BodyPath)
		if result.Type != gjson.String {
			continue
		}
		if id := cleanSessionID(applyTransform(strings.TrimSpace(result.Str), rule.Transform)); id != "" {
			return id
		}
	}
	return ""
}

// scopeSessionID namespaces every client-provided id by user path. UUID syntax
// does not prove uniqueness because the value is still caller-controlled.
// The id and path are hashed as distinct values — plain concatenation would be
// ambiguous, since both parts are client-influenced.
func scopeSessionID(id, userPath string) string {
	if userPath == "" {
		return id
	}
	sum := sha256.Sum256([]byte(userPath + "\x00" + id))
	return "scoped-" + hex.EncodeToString(sum[:16])
}

// contentAnchor is the stable prefix of a conversation used to derive a
// session id when the client sends no explicit signal: the model, the system
// context, and the leading messages through the first user turn. Follow-up
// requests in the same conversation resend this prefix unchanged, so they
// hash to the same id — including the very first request, which carries no
// later turns yet.
type contentAnchor struct {
	UserPath     string            `json:"user_path,omitempty"`
	Model        json.RawMessage   `json:"model,omitempty"`
	System       json.RawMessage   `json:"system,omitempty"`
	Instructions json.RawMessage   `json:"instructions,omitempty"`
	Opening      []json.RawMessage `json:"opening,omitempty"`
	Tools        json.RawMessage   `json:"tools,omitempty"`
}

// contentSessionID derives a session id from the conversation prefix of chat
// and responses requests (precedent: the xAI provider's generated Grok
// conversation id). Other operations return "".
func contentSessionID(snapshot *core.RequestSnapshot, body []byte, userPath string) string {
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
	const prefix = "auto-"
	var id [len(prefix) + 32]byte
	copy(id[:], prefix)
	hex.Encode(id[len(prefix):], sum[:16])
	return string(id[:])
}

// maxOpeningMessages bounds the anchor when a conversation opens with an
// unusually long non-user preamble.
const maxOpeningMessages = 8

// openingMessages collects the leading messages through the first user turn.
// That prefix is identical on every request of a conversation regardless of
// how many later turns it has accumulated, while two conversations with the
// same system prompt still diverge on their first user message.
func openingMessages(messages gjson.Result) []json.RawMessage {
	var opening []json.RawMessage
	messages.ForEach(func(_, message gjson.Result) bool {
		opening = append(opening, canonicalSegment(message))
		if message.Get("role").Str == "user" || len(opening) >= maxOpeningMessages {
			return false
		}
		return true
	})
	return opening
}
