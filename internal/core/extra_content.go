package core

import (
	"bytes"

	"github.com/goccy/go-json"
)

// ExtraContentField names the member that carries provider state through the
// canonical chat types: opaque data a provider returned and expects back
// verbatim on a later turn, such as Gemini thought signatures or Anthropic
// thinking blocks. It lives on messages, tool calls, and Responses items as
// one object keyed by vendor, for example {"google": {...}}.
//
// The rules are the same for every provider:
//   - translation layers copy the member through untouched;
//   - the router drops every vendor the selected provider does not own;
//   - only the owning provider adapter reads or writes inside its object.
const ExtraContentField = "extra_content"

// Vendors that own an extra_content object.
const (
	ExtraContentVendorGoogle    = "google"
	ExtraContentVendorAnthropic = "anthropic"
)

// Members of extra_content.anthropic, set by the Anthropic Messages ingress
// and consumed by the Anthropic provider.
const (
	// ThinkingBlocksField holds the raw thinking/redacted_thinking blocks of an
	// assistant turn as a JSON array. Anthropic requires them back verbatim
	// (with signatures) when a thinking-enabled tool-use turn continues.
	ThinkingBlocksField = "thinking_blocks"
	// ToolResultIsErrorField marks a tool message as a failed tool call
	// (Anthropic tool_result.is_error).
	ToolResultIsErrorField = "is_error"
)

// ExtraContent returns the vendor's object under extra_content, or nil when
// the member is absent, is not an object, or has no entry for the vendor.
func (fields UnknownJSONFields) ExtraContent(vendor string) json.RawMessage {
	return extraContentVendors(fields.Lookup(ExtraContentField))[vendor]
}

// WithExtraContent returns fields with extra_content.<vendor> set to value.
// Other vendors' objects and every other member are kept.
func (fields UnknownJSONFields) WithExtraContent(vendor string, value json.RawMessage) (UnknownJSONFields, error) {
	vendors := extraContentVendors(fields.Lookup(ExtraContentField))
	if vendors == nil {
		vendors = make(map[string]json.RawMessage, 1)
	}
	vendors[vendor] = value
	encoded, err := json.Marshal(vendors)
	if err != nil {
		return UnknownJSONFields{}, err
	}
	return MergeUnknownJSONFields(fields, map[string]json.RawMessage{ExtraContentField: encoded})
}

// HasForeignExtraContent reports whether WithoutForeignExtraContent(keep)
// would remove anything.
func (fields UnknownJSONFields) HasForeignExtraContent(keep string) bool {
	raw := fields.Lookup(ExtraContentField)
	if len(raw) == 0 {
		return false
	}
	vendors := extraContentVendors(raw)
	if _, ok := vendors[keep]; !ok || keep == "" {
		return true
	}
	return len(vendors) > 1
}

// WithoutForeignExtraContent returns fields with every extra_content vendor
// other than keep removed. The member disappears when nothing remains, so a
// provider with no state of its own (keep == "") never sees it.
func (fields UnknownJSONFields) WithoutForeignExtraContent(keep string) UnknownJSONFields {
	if !fields.HasForeignExtraContent(keep) {
		return fields
	}
	kept := KeepExtraContentVendor(fields.Lookup(ExtraContentField), keep)
	if kept == nil {
		return fields.Without(ExtraContentField)
	}
	merged, err := MergeUnknownJSONFields(fields, map[string]json.RawMessage{ExtraContentField: kept})
	if err != nil {
		return fields.Without(ExtraContentField)
	}
	return merged
}

// KeepExtraContentVendor reduces a raw extra_content value to the keep
// vendor's object. It returns nil when nothing should remain: the vendor is
// absent, keep is empty, or the value is not an object.
func KeepExtraContentVendor(raw json.RawMessage, keep string) json.RawMessage {
	kept, ok := extraContentVendors(raw)[keep]
	if !ok || keep == "" {
		return nil
	}
	encoded, err := json.Marshal(map[string]json.RawMessage{keep: kept})
	if err != nil {
		return nil
	}
	return encoded
}

// extraContentVendors decodes an extra_content member. Anything but a JSON
// object carries no provider state and decodes to nil.
func extraContentVendors(raw json.RawMessage) map[string]json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var vendors map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &vendors); err != nil {
		return nil
	}
	return vendors
}
