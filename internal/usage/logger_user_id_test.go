package usage

import (
	"testing"
	"time"
)

func TestLoggerStampsUserIDFromResolver(t *testing.T) {
	store := &mockStore{}
	logger := NewLogger(store, Config{Enabled: true, BufferSize: 10, FlushInterval: 10 * time.Millisecond})
	defer logger.Close()

	logger.SetUserIDResolver(func(userPath string) string {
		if userPath == "/team/alpha" {
			return "user-1"
		}
		return ""
	})

	logger.Write(&UsageEntry{ID: "resolved", UserPath: "/team/alpha"})
	logger.Write(&UsageEntry{ID: "unknown-path", UserPath: "/sales"})
	logger.Write(&UsageEntry{ID: "no-path"})
	logger.Write(&UsageEntry{ID: "pre-stamped", UserPath: "/team/alpha", UserID: "explicit"})

	deadline := time.After(2 * time.Second)
	for {
		entries := store.getEntries()
		if len(entries) == 4 {
			byID := map[string]string{}
			for _, entry := range entries {
				byID[entry.ID] = entry.UserID
			}
			if byID["resolved"] != "user-1" {
				t.Fatalf("resolved entry UserID = %q, want user-1", byID["resolved"])
			}
			if byID["unknown-path"] != "" || byID["no-path"] != "" {
				t.Fatalf("unresolvable entries stamped: %v", byID)
			}
			if byID["pre-stamped"] != "explicit" {
				t.Fatalf("pre-stamped entry UserID = %q, want explicit", byID["pre-stamped"])
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for entries: got %d, want 4", len(entries))
		case <-time.After(10 * time.Millisecond):
		}
	}
}
