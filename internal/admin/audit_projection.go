package admin

import "github.com/enterpilot/gomodel/internal/auditlog"

// Audit list projection.
//
// A list page ships up to 100 entries, and a full entry carries the original
// request body, the response body, per-attempt upstream error bodies, and one
// rewritten body per request revision — megabytes per row for agent traffic.
// The dashboard's collapsed row renders none of that: it needs scalar
// metadata, the attempt pips, and two derived signals (can this entry open
// the Interactions drawer, and has the request settled). The expanded row
// lazy-loads the full entry from /admin/audit/detail.
//
// slimAuditListEntry therefore strips the heavy payloads from a list entry
// and records what the client needs to compensate:
//   - BodiesOmitted tells the dashboard to fetch the detail endpoint on
//     expand (and that the entry is persisted, i.e. not in-flight).
//   - ConversationPayload preserves the drawer-eligibility signal that the
//     client would otherwise sniff from the removed bodies.
//
// The detail and conversation endpoints are not slimmed this way: detail is
// the designated source of full payloads, and the conversation thread is
// built client-side from request/response bodies (see slimConversationEntry
// for what it drops instead).
func slimAuditListEntry(resp *auditLogEntryResponse) {
	d := resp.Data
	if d == nil {
		return
	}

	stripped := false
	slim := *d

	if slim.RequestBody != nil || slim.ResponseBody != nil {
		resp.ConversationPayload = hasConversationPayload(slim.RequestBody, slim.ResponseBody)
	}

	if slim.RequestBody != nil {
		slim.RequestBody = nil
		stripped = true
	}
	if slim.ResponseBody != nil {
		slim.ResponseBody = nil
		stripped = true
	}

	if len(slim.Attempts) > 0 {
		attempts := make([]auditlog.AttemptSnapshot, len(slim.Attempts))
		copy(attempts, slim.Attempts)
		for i := range attempts {
			if attempts[i].ResponseBody != nil || attempts[i].ResponseHeaders != nil {
				attempts[i].ResponseBody = nil
				attempts[i].ResponseHeaders = nil
				stripped = true
			}
		}
		slim.Attempts = attempts
	}

	if len(slim.RequestRevisions) > 0 {
		revisions := make([]auditlog.RequestRevisionSnapshot, len(slim.RequestRevisions))
		copy(revisions, slim.RequestRevisions)
		for i := range revisions {
			if revisions[i].Body != nil {
				revisions[i].Body = nil
				stripped = true
			}
		}
		slim.RequestRevisions = revisions
	}

	if !stripped {
		return
	}
	resp.Data = &slim
	resp.BodiesOmitted = true
}

// hasConversationPayload mirrors the dashboard's body sniff for entries whose
// path alone does not qualify them for the Interactions drawer (for example
// /v1/messages or provider passthrough): a request shaped like a conversation
// or a response shaped like model output.
func hasConversationPayload(requestBody, responseBody any) bool {
	if req, ok := auditlog.BodyDocument(requestBody).(map[string]any); ok {
		if _, ok := req["messages"].([]any); ok {
			return true
		}
		if _, ok := req["input"]; ok {
			return true
		}
		if _, ok := req["instructions"].(string); ok {
			return true
		}
		if _, ok := req["previous_response_id"].(string); ok {
			return true
		}
	}
	if resp, ok := auditlog.BodyDocument(responseBody).(map[string]any); ok {
		if _, ok := resp["choices"].([]any); ok {
			return true
		}
		if looksLikeResponsesOutput(resp["output"]) {
			return true
		}
	}
	return false
}

// looksLikeResponsesOutput reports whether v is a Responses-API output array:
// a non-empty array of typed items.
func looksLikeResponsesOutput(v any) bool {
	items, ok := v.([]any)
	if !ok || len(items) == 0 {
		return false
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		return false
	}
	_, hasType := first["type"]
	_, hasContent := first["content"]
	return hasType || hasContent
}

// slimConversationEntry strips the fields the Interactions drawer never
// reads from a conversation-thread entry. Request headers are retained because
// the drawer uses the safe, redacted subset to continue the same session from
// the original endpoint. Response headers and attempts only inflate the
// response. Request revisions keep their metadata — the drawer's request-step
// picker needs to know which rewrites ran — but drop the heavy per-revision
// bodies and details; the drawer lazy-loads those from /admin/audit/detail.
func slimConversationEntry(entry *auditlog.LogEntry) {
	d := entry.Data
	if d == nil {
		return
	}
	if d.Attempts == nil && d.RequestRevisions == nil && d.ResponseHeaders == nil {
		return
	}
	slim := *d
	slim.Attempts = nil
	slim.ResponseHeaders = nil
	if len(slim.RequestRevisions) > 0 {
		revisions := make([]auditlog.RequestRevisionSnapshot, len(slim.RequestRevisions))
		copy(revisions, slim.RequestRevisions)
		for i := range revisions {
			revisions[i].Body = nil
			revisions[i].Detail = nil
		}
		slim.RequestRevisions = revisions
	}
	entry.Data = &slim
}
