package versioncheck

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSplitVisit(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		wantDate string
		wantID   string
	}{
		{"well formed", "2026-08-26-3f2504e0-4f89-11d3-9a0c-0305e82c3301", "2026-08-26", "3f2504e0-4f89-11d3-9a0c-0305e82c3301"},
		{"uppercase uuid is not canonical", "2026-08-26-3F2504E0-4F89-11D3-9A0C-0305E82C3301", "", ""},
		{"empty", "", "", ""},
		{"no id", "2026-08-26", "", ""},
		{"not a date", "hello-world-abc", "", ""},
		{"impossible date", "2026-13-45-abc", "", ""},
		{"missing separator", "2026-08-26xabc", "", ""},
		// The id is echoed into an outbound header and back into Set-Cookie,
		// so anything but the canonical UUID NewVisit emits is discarded.
		{"free text id", "2026-08-26-not-a-uuid", "", ""},
		{"oversized id", "2026-08-26-" + strings.Repeat("A", 200), "", ""},
		{"braced uuid", "2026-08-26-{3f2504e0-4f89-11d3-9a0c-0305e82c3301}", "", ""},
		{"urn uuid", "2026-08-26-urn:uuid:3f2504e0-4f89-11d3-9a0c-0305e82c3301", "", ""},
		{"unhyphenated uuid", "2026-08-26-3f2504e04f8911d39a0c0305e82c3301", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			date, id := SplitVisit(tt.value)
			require.Equal(t, tt.wantDate, date)
			require.Equal(t, tt.wantID, id, "SplitVisit(%q) = (%q, %q), want (%q, %q)", tt.value, date, id, tt.wantDate, tt.wantID)

		})
	}
}

func TestDueToday(t *testing.T) {
	now := time.Date(2026, 8, 26, 9, 30, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"first visit", "", true},
		{"checked yesterday", "2026-08-25-3f2504e0-4f89-11d3-9a0c-0305e82c3301", true},
		{"checked today", "2026-08-26-3f2504e0-4f89-11d3-9a0c-0305e82c3301", false},
		{"malformed cookie", "garbage", true},
		{"non-uuid id", "2026-08-26-abc", true},
		{"date without an id", "2026-08-26", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DueToday(tt.value, now)
			require.Equal(t, tt.want, got, "DueToday(%q) = %v, want %v", tt.value, got, tt.want)

		})
	}
}

func TestNewVisitKeepsTheBrowserID(t *testing.T) {
	now := time.Date(2026, 8, 26, 9, 30, 0, 0, time.UTC)

	value := NewVisit("3f2504e0-4f89-11d3-9a0c-0305e82c3301", now)
	require.Equal(t, "2026-08-26-3f2504e0-4f89-11d3-9a0c-0305e82c3301", value)

	minted := NewVisit("", now)
	date, id := SplitVisit(minted)
	require.Equal(t, "2026-08-26", date)
	require.NotEmpty(t, id)
	other := NewVisit("", now)
	require.NotEqual(t, minted, other)
}
