package auditlog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestExtractStringField(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		v    any
		key  string
		want string
	}{
		{
			name: "map payload",
			v: map[string]any{
				"id": " resp_1 ",
			},
			key:  "id",
			want: "resp_1",
		},
		{
			name: "bson m payload",
			v: bson.M{
				"id": " resp_2 ",
			},
			key:  "id",
			want: "resp_2",
		},
		{
			name: "bson d payload",
			v: bson.D{
				{Key: "id", Value: " resp_3 "},
			},
			key:  "id",
			want: "resp_3",
		},
		{
			name: "missing key",
			v: bson.D{
				{Key: "other", Value: "x"},
			},
			key:  "id",
			want: "",
		},
		{
			name: "non string value",
			v: bson.M{
				"id": 123,
			},
			key:  "id",
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := extractStringField(tc.v, tc.key); got != tc.want {
				t.Fatalf("extractStringField() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractConversationIDsFromBSONBodies(t *testing.T) {
	t.Parallel()

	entry := &LogEntry{
		Data: &LogData{
			RequestBody: bson.D{
				{Key: "previous_response_id", Value: " resp_prev "},
			},
			ResponseBody: bson.D{
				{Key: "id", Value: " resp_cur "},
			},
		},
	}

	if got := extractPreviousResponseID(entry); got != "resp_prev" {
		t.Fatalf("extractPreviousResponseID() = %q, want %q", got, "resp_prev")
	}
	if got := extractResponseID(entry); got != "resp_cur" {
		t.Fatalf("extractResponseID() = %q, want %q", got, "resp_cur")
	}
}

// chainEntry builds a log entry linked into a response chain: it replays
// prevRespID as request_body.previous_response_id and answers with
// response_body.id = respID.
func chainEntry(id, prevRespID, respID string, at time.Time) *LogEntry {
	data := &LogData{}
	if prevRespID != "" {
		data.RequestBody = map[string]any{"previous_response_id": prevRespID}
	}
	if respID != "" {
		data.ResponseBody = map[string]any{"id": respID}
	}
	return &LogEntry{ID: id, Timestamp: at, Data: data}
}

func TestBuildConversationThreadWalksBothDirections(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	// log-1 → log-2 → log-3, anchored in the middle.
	byID := map[string]*LogEntry{
		"log-2": chainEntry("log-2", "resp-1", "resp-2", base.Add(time.Minute)),
	}
	byRespID := map[string]*LogEntry{
		"resp-1": chainEntry("log-1", "", "resp-1", base),
	}
	byPrevRespID := map[string]*LogEntry{
		"resp-2": chainEntry("log-3", "resp-2", "resp-3", base.Add(2*time.Minute)),
	}

	result, err := buildConversationThread(context.Background(), "log-2", 40,
		func(_ context.Context, id string) (*LogEntry, error) { return byID[id], nil },
		func(_ context.Context, id string) (*LogEntry, error) { return byRespID[id], nil },
		func(_ context.Context, id string) (*LogEntry, error) { return byPrevRespID[id], nil },
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Truncated {
		t.Error("complete walk must not be marked truncated")
	}
	if len(result.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(result.Entries))
	}
	for i, want := range []string{"log-1", "log-2", "log-3"} {
		if result.Entries[i].ID != want {
			t.Errorf("entry %d = %s, want %s", i, result.Entries[i].ID, want)
		}
	}
}

func TestBuildSessionConversationFollowsLatestAndKeepsAnchor(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	anchor := &LogEntry{ID: "log-1", SessionID: "session-1", UserPath: "/team/a", Timestamp: base}
	newestFirst := []LogEntry{
		{ID: "log-4", SessionID: "session-1", Timestamp: base.Add(3 * time.Minute)},
		{ID: "log-3", SessionID: "session-1", Timestamp: base.Add(2 * time.Minute)},
		{ID: "log-2", SessionID: "session-1", Timestamp: base.Add(time.Minute)},
		*anchor,
	}

	result, err := buildSessionConversation(context.Background(), anchor, 3,
		func(_ context.Context, params LogQueryParams) (*LogListResult, error) {
			if params.SessionID != "session-1" {
				t.Fatalf("session id = %q, want session-1", params.SessionID)
			}
			if params.UserPath != "/team/a" {
				t.Fatalf("user path = %q, want /team/a", params.UserPath)
			}
			if !params.ExactUserPath {
				t.Fatal("session conversation must use an exact user-path filter")
			}
			if !params.OmitAttempts {
				t.Fatal("session conversation must omit attempt hydration")
			}
			end := min(params.Offset+params.Limit, len(newestFirst))
			return &LogListResult{
				Entries: newestFirst[params.Offset:end],
				Total:   len(newestFirst),
			}, nil
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Truncated {
		t.Fatal("limited session must be marked truncated")
	}
	if result.AnchorID != "log-1" {
		t.Fatalf("anchor id = %q, want log-1", result.AnchorID)
	}
	got := []string{result.Entries[0].ID, result.Entries[1].ID, result.Entries[2].ID}
	want := []string{"log-1", "log-3", "log-4"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry ids = %v, want %v", got, want)
		}
	}
}

func TestBuildSessionConversationPagesPastAuditListCap(t *testing.T) {
	t.Parallel()
	anchor := &LogEntry{ID: "log-119", SessionID: "session-1", Timestamp: time.Now()}
	calls := 0
	result, err := buildSessionConversation(context.Background(), anchor, 120,
		func(_ context.Context, params LogQueryParams) (*LogListResult, error) {
			calls++
			entries := make([]LogEntry, params.Limit)
			for i := range entries {
				entries[i] = LogEntry{ID: fmt.Sprintf("log-%d", params.Offset+i)}
			}
			return &LogListResult{Entries: entries, Total: 120}, nil
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 || len(result.Entries) != 120 {
		t.Fatalf("calls/entries = %d/%d, want 2/120", calls, len(result.Entries))
	}
}

// A deadline expiring mid-walk must yield the partial thread collected so
// far, marked Truncated — not an error. Before this behavior one slow chain
// hop held /admin/audit/conversation open until a fronting proxy killed it.
func TestBuildConversationThreadReturnsPartialOnDeadline(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	anchor := chainEntry("log-2", "resp-1", "resp-2", base)

	t.Run("backward hop times out", func(t *testing.T) {
		result, err := buildConversationThread(context.Background(), "log-2", 40,
			func(_ context.Context, _ string) (*LogEntry, error) { return anchor, nil },
			func(_ context.Context, _ string) (*LogEntry, error) { return nil, context.DeadlineExceeded },
			func(_ context.Context, _ string) (*LogEntry, error) {
				t.Fatal("forward walk must not run once the deadline expired")
				return nil, nil
			},
		)
		if err != nil {
			t.Fatalf("deadline mid-walk must not fail the build: %v", err)
		}
		if !result.Truncated {
			t.Error("partial thread must be marked truncated")
		}
		if len(result.Entries) != 1 || result.Entries[0].ID != "log-2" {
			t.Errorf("expected the anchor alone, got %+v", result.Entries)
		}
	})

	t.Run("forward hop times out", func(t *testing.T) {
		parent := chainEntry("log-1", "", "resp-1", base.Add(-time.Minute))
		result, err := buildConversationThread(context.Background(), "log-2", 40,
			func(_ context.Context, _ string) (*LogEntry, error) { return anchor, nil },
			func(_ context.Context, _ string) (*LogEntry, error) { return parent, nil },
			func(ctx context.Context, _ string) (*LogEntry, error) { return nil, context.Canceled },
		)
		if err != nil {
			t.Fatalf("deadline mid-walk must not fail the build: %v", err)
		}
		if !result.Truncated {
			t.Error("partial thread must be marked truncated")
		}
		if len(result.Entries) != 2 {
			t.Errorf("expected anchor plus backward entry, got %+v", result.Entries)
		}
	})

	t.Run("anchor lookup failure still errors", func(t *testing.T) {
		_, err := buildConversationThread(context.Background(), "log-2", 40,
			func(_ context.Context, _ string) (*LogEntry, error) { return nil, context.DeadlineExceeded },
			func(_ context.Context, _ string) (*LogEntry, error) { return nil, nil },
			func(_ context.Context, _ string) (*LogEntry, error) { return nil, nil },
		)
		if err == nil {
			t.Fatal("a thread without its anchor is not partial, it is missing — must error")
		}
	})
}
