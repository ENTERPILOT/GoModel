package auditlog

import (
	"context"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type entryLookup func(ctx context.Context, id string) (*LogEntry, error)

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

	current := anchor
	for len(thread) < limit {
		prevID := extractPreviousResponseID(current)
		if prevID == "" {
			break
		}
		parent, err := findByResponseID(ctx, prevID)
		if err != nil {
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
	for len(thread) < limit {
		respID := extractResponseID(current)
		if respID == "" {
			break
		}
		child, err := findByPreviousResponseID(ctx, respID)
		if err != nil {
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
		AnchorID: anchor.ID,
		Entries:  entries,
	}, nil
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
