package auditlog

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			require.Equal(t, tc.want, extractStringField(tc.v, tc.key))
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
	require.Equal(t, "resp_prev", extractPreviousResponseID(entry))
	require.Equal(t, "resp_cur", extractResponseID(entry))
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
	require.NoError(t, err)
	assert.False(t, result.Truncated)
	require.Len(t, result.Entries, 3)

	for i, want := range []string{"log-1", "log-2", "log-3"} {
		assert.Equal(t, want, result.Entries[i].ID)
	}
}

func TestBuildSessionConversationKeepsAnchorAndClosestEntries(t *testing.T) {
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
			require.Equal(t, "session-1", params.SessionID)
			require.Equal(t, "/team/a", params.UserPath)
			require.True(t, params.ExactUserPath)
			require.True(t, params.OmitAttempts)

			end := min(params.Offset+params.Limit, len(newestFirst))
			return &LogListResult{
				Entries: newestFirst[params.Offset:end],
				Total:   len(newestFirst),
			}, nil
		})
	require.NoError(t, err)
	require.True(t, result.Truncated)
	require.Equal(t, "log-1", result.AnchorID)

	require.Len(t, result.Entries, 3)
	require.Equal(t, []string{"log-1", "log-2", "log-3"}, []string{result.Entries[0].ID, result.Entries[1].ID, result.Entries[2].ID})
}

func TestBuildSessionConversationDeduplicatesOverlappingPages(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	anchor := &LogEntry{ID: "log-a", SessionID: "session-1", Timestamp: base}

	result, err := buildSessionConversation(context.Background(), anchor, 4,
		func(_ context.Context, params LogQueryParams) (*LogListResult, error) {
			switch params.beforeID {
			case "":
				return &LogListResult{Entries: []LogEntry{
					{ID: "log-c", Timestamp: base.Add(2 * time.Second)},
					{ID: "log-b", Timestamp: base.Add(time.Second)},
					*anchor,
				}, Total: 4}, nil
			case anchor.ID:
				return &LogListResult{Entries: []LogEntry{
					*anchor,
					{ID: "log-old", Timestamp: base.Add(-time.Second)},
				}, Total: 4}, nil
			default:
				t.Fatalf("unexpected cursor %q", params.beforeID)
				return nil, nil
			}
		})
	require.NoError(t, err)

	got := []string{result.Entries[0].ID, result.Entries[1].ID, result.Entries[2].ID, result.Entries[3].ID}
	want := []string{"log-old", "log-a", "log-b", "log-c"}
	require.Equal(t, want, got)
}

func TestBuildSessionConversationOrdersEqualTimestampsByID(t *testing.T) {
	t.Parallel()
	timestamp := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	anchor := &LogEntry{ID: "log-a", SessionID: "session-1", Timestamp: timestamp}
	result, err := buildSessionConversation(context.Background(), anchor, 2,
		func(_ context.Context, _ LogQueryParams) (*LogListResult, error) {
			return &LogListResult{Entries: []LogEntry{
				{ID: "log-b", Timestamp: timestamp},
				*anchor,
			}, Total: 2}, nil
		})
	require.NoError(t, err)
	require.Equal(t, "log-a", result.Entries[0].ID)
	require.Equal(t, "log-b", result.Entries[1].ID, "equal-timestamp entries = %+v, want log-a then log-b", result.Entries)
}

func TestBuildSessionConversationPagesPastAuditListCap(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	all := make([]LogEntry, 120)
	for i := range all {
		index := 119 - i
		all[i] = LogEntry{ID: fmt.Sprintf("log-%03d", index), Timestamp: base.Add(time.Duration(index) * time.Second)}
	}
	anchor := &LogEntry{ID: "log-119", SessionID: "session-1", Timestamp: base.Add(119 * time.Second)}
	calls := 0
	result, err := buildSessionConversation(context.Background(), anchor, 120,
		func(_ context.Context, params LogQueryParams) (*LogListResult, error) {
			calls++
			if calls == 2 {
				all = append([]LogEntry{{ID: "live-new", Timestamp: base.Add(time.Hour)}}, all...)
			}
			eligible := make([]LogEntry, 0, len(all))
			for _, entry := range all {
				if !params.beforeTimestamp.IsZero() &&
					(entry.Timestamp.After(params.beforeTimestamp) ||
						(entry.Timestamp.Equal(params.beforeTimestamp) && entry.ID >= params.beforeID)) {
					continue
				}
				eligible = append(eligible, entry)
			}
			return &LogListResult{Entries: eligible[:min(params.Limit, len(eligible))], Total: len(all)}, nil
		})
	require.NoError(t, err)
	require.Equal(t, 2, calls)
	require.Len(t, result.Entries, 120)

	seen := make(map[string]struct{}, len(result.Entries))
	for _, entry := range result.Entries {
		require.NotEqual(t, "live-new", entry.ID)
		_, duplicate := seen[entry.ID]
		require.False(t, duplicate)

		seen[entry.ID] = struct{}{}
	}
}

func TestBuildSessionConversationBoundaries(t *testing.T) {
	t.Parallel()
	lookupErr := errors.New("lookup failed")
	tests := []struct {
		name       string
		anchor     *LogEntry
		page       *LogListResult
		lookupErr  error
		wantError  bool
		wantCalled bool
		wantPath   string
	}{
		{name: "nil anchor"},
		{
			name:       "root user path",
			anchor:     &LogEntry{ID: "root", SessionID: "session"},
			page:       &LogListResult{},
			wantCalled: true,
			wantPath:   "/",
		},
		{
			name:       "lookup error",
			anchor:     &LogEntry{ID: "error", SessionID: "session", UserPath: "/team"},
			lookupErr:  lookupErr,
			wantError:  true,
			wantCalled: true,
			wantPath:   "/team",
		},
		{
			name:       "nil page",
			anchor:     &LogEntry{ID: "nil-page", SessionID: "session", UserPath: "/team"},
			wantCalled: true,
			wantPath:   "/team",
		},
		{
			name:       "empty page",
			anchor:     &LogEntry{ID: "empty-page", SessionID: "session", UserPath: "/team"},
			page:       &LogListResult{Entries: []LogEntry{}, Total: 0},
			wantCalled: true,
			wantPath:   "/team",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			result, err := buildSessionConversation(context.Background(), tc.anchor, 40,
				func(_ context.Context, params LogQueryParams) (*LogListResult, error) {
					called = true
					require.Equal(t, tc.wantPath, params.UserPath)
					require.True(t, params.ExactUserPath)
					require.True(t, params.OmitAttempts, "lookup params = %+v", params)

					return tc.page, tc.lookupErr
				})
			if tc.wantError {
				require.ErrorIs(t, err, lookupErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.wantCalled, called)

			if tc.wantError {
				return
			}
			if tc.anchor == nil {
				require.NotNil(t, result)
				require.Empty(t, result.Entries)

				return
			}
			require.Equal(t, tc.anchor.ID, result.AnchorID)
			require.Len(t, result.Entries, 1)
			require.Equal(t, tc.anchor.ID, result.Entries[0].ID)
		})
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
		require.NoError(t, err)
		assert.True(t, result.Truncated)
		require.Len(t, result.Entries, 1)
		assert.Equal(t, "log-2", result.Entries[0].ID)
	})

	t.Run("forward hop times out", func(t *testing.T) {
		parent := chainEntry("log-1", "", "resp-1", base.Add(-time.Minute))
		result, err := buildConversationThread(context.Background(), "log-2", 40,
			func(_ context.Context, _ string) (*LogEntry, error) { return anchor, nil },
			func(_ context.Context, _ string) (*LogEntry, error) { return parent, nil },
			func(ctx context.Context, _ string) (*LogEntry, error) { return nil, context.Canceled },
		)
		require.NoError(t, err)
		assert.True(t, result.Truncated)
		assert.Len(t, result.Entries, 2)
	})

	t.Run("anchor lookup failure still errors", func(t *testing.T) {
		_, err := buildConversationThread(context.Background(), "log-2", 40,
			func(_ context.Context, _ string) (*LogEntry, error) { return nil, context.DeadlineExceeded },
			func(_ context.Context, _ string) (*LogEntry, error) { return nil, nil },
			func(_ context.Context, _ string) (*LogEntry, error) { return nil, nil },
		)
		require.Error(t, err)
	})
}
