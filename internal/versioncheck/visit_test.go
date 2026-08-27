package versioncheck

import (
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
		{"well formed", "2026-08-26-9f1c2d", "2026-08-26", "9f1c2d"},
		{"uuid id", "2026-08-26-3f2504e0-4f89-11d3-9a0c-0305e82c3301", "2026-08-26", "3f2504e0-4f89-11d3-9a0c-0305e82c3301"},
		{"empty", "", "", ""},
		{"no id", "2026-08-26", "", ""},
		{"not a date", "hello-world-abc", "", ""},
		{"impossible date", "2026-13-45-abc", "", ""},
		{"missing separator", "2026-08-26xabc", "", ""},
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
		{"checked yesterday", "2026-08-25-abc", true},
		{"checked today", "2026-08-26-abc", false},
		{"malformed cookie", "garbage", true},
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

	value := NewVisit("abc", now)
	if value != "2026-08-26-abc" {
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
