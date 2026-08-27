package versioncheck

import (
	"strings"
	"testing"
	"time"
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
			if date != tt.wantDate || id != tt.wantID {
				t.Fatalf("SplitVisit(%q) = (%q, %q), want (%q, %q)", tt.value, date, id, tt.wantDate, tt.wantID)
			}
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
			if got := DueToday(tt.value, now); got != tt.want {
				t.Fatalf("DueToday(%q) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

func TestNewVisitKeepsTheBrowserID(t *testing.T) {
	now := time.Date(2026, 8, 26, 9, 30, 0, 0, time.UTC)

	value := NewVisit("3f2504e0-4f89-11d3-9a0c-0305e82c3301", now)
	if value != "2026-08-26-3f2504e0-4f89-11d3-9a0c-0305e82c3301" {
		t.Fatalf("NewVisit = %q, want the id carried into today", value)
	}

	minted := NewVisit("", now)
	date, id := SplitVisit(minted)
	if date != "2026-08-26" || id == "" {
		t.Fatalf("NewVisit with no id = %q, want a fresh id stamped today", minted)
	}
	if other := NewVisit("", now); other == minted {
		t.Fatal("expected a distinct id for each first visit")
	}
}
