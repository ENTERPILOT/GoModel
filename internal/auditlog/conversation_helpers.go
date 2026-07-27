package auditlog

import (
	"context"
	"errors"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type entryLookup func(ctx context.Context, id string) (*LogEntry, error)

// buildConversationThread walks the response-ID chain outward from the anchor
// entry: backward via previous_response_id, then forward via the entries that
// chain onto the anchor's response ID.
//
// The anchor lookup is load-bearing — without it there is no thread, so its
// error (including a deadline) fails the build. Every later hop is an
// extension: when the caller's deadline expires mid-walk, the thread built so
// far is returned with Truncated set instead of an error. The drawer then
// renders the turns nearest the anchor rather than nothing — before this,
// one slow hop could hold the request open until a fronting proxy killed it.
func buildConversationThread(ctx context.Context, logID string, limit int, getByID func(ctx context.Context, id string) (*LogEntry, error), findByResponseID, findByPreviousResponseID entryLookup) (*ConversationResult, error) {
	limit = clampConversationLimit(limit)

	anchor, err := getByID(ctx, logID)
	if err != nil {
		return nil, err
	}
	if anchor == nil {
		return &ConversationResult{
			AnchorID: logID,
			Entries:  []LogEntry{},
		}, nil
	}

	thread := []*LogEntry{anchor}
	seen := map[string]struct{}{anchor.ID: {}}
	truncated := false

	current := anchor
	for len(thread) < limit {
		prevID := extractPreviousResponseID(current)
		if prevID == "" {
			break
		}
		parent, err := findByResponseID(ctx, prevID)
		if err != nil {
			if deadlineExpired(ctx, err) {
				truncated = true
				break
			}
			return nil, err
		}
		if parent == nil {
			break
		}
		if _, ok := seen[parent.ID]; ok {
			break
		}
		thread = append([]*LogEntry{parent}, thread...)
		seen[parent.ID] = struct{}{}
		current = parent
	}

	current = anchor
	for !truncated && len(thread) < limit {
		respID := extractResponseID(current)
		if respID == "" {
			break
		}
		child, err := findByPreviousResponseID(ctx, respID)
		if err != nil {
			if deadlineExpired(ctx, err) {
				truncated = true
				break
			}
			return nil, err
		}
		if child == nil {
			break
		}
		if _, ok := seen[child.ID]; ok {
			break
		}
		thread = append(thread, child)
		seen[child.ID] = struct{}{}
		current = child
	}

	sort.Slice(thread, func(i, j int) bool {
		return thread[i].Timestamp.Before(thread[j].Timestamp)
	})

	entries := make([]LogEntry, 0, len(thread))
	for _, entry := range thread {
		if entry != nil {
			entries = append(entries, *entry)
		}
	}

	return &ConversationResult{
		AnchorID:  anchor.ID,
		Entries:   entries,
		Truncated: truncated,
	}, nil
}

// deadlineExpired reports whether a lookup failure is attributable to the
// caller's context rather than the store: either the error itself is a
// context error, or the context expired while the query ran (drivers commonly
// wrap or replace the cancellation cause, so the context is checked too).
func deadlineExpired(ctx context.Context, err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	return ctx.Err() != nil
}

func extractResponseID(entry *LogEntry) string {
	if entry == nil || entry.Data == nil {
		return ""
	}
	return extractStringField(entry.Data.ResponseBody, "id")
}

func extractPreviousResponseID(entry *LogEntry) string {
	if entry == nil || entry.Data == nil {
		return ""
	}
	return extractStringField(entry.Data.RequestBody, "previous_response_id")
}

func extractStringField(v any, key string) string {
	switch obj := v.(type) {
	case map[string]any:
		return extractTrimmedString(obj[key])
	case bson.M:
		return extractTrimmedString(obj[key])
	case bson.D:
		for _, entry := range obj {
			if entry.Key == key {
				return extractTrimmedString(entry.Value)
			}
		}
		return ""
	default:
		return ""
	}
}

func extractTrimmedString(raw any) string {
	if raw == nil {
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func clampConversationLimit(limit int) int {
	if limit <= 0 {
		return 40
	}
	if limit > 200 {
		return 200
	}
	return limit
}
