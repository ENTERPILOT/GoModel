package ratelimit

import (
	"strings"
	"testing"
)

func TestNormalizeRule(t *testing.T) {
	tests := []struct {
		name    string
		rule    Rule
		wantErr string
		check   func(t *testing.T, rule Rule)
	}{
		{
			name: "normalizes path and keeps limits",
			rule: Rule{Subject: "team/alpha/", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(10)},
			check: func(t *testing.T, rule Rule) {
				if rule.Subject != "/team/alpha" {
					t.Fatalf("user path = %q, want /team/alpha", rule.Subject)
				}
				if rule.CreatedAt.IsZero() || rule.UpdatedAt.IsZero() {
					t.Fatal("timestamps not set")
				}
			},
		},
		{
			name: "empty path becomes root",
			rule: Rule{Subject: "", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: int64Ptr(100)},
			check: func(t *testing.T, rule Rule) {
				if rule.Subject != "/" {
					t.Fatalf("user path = %q, want /", rule.Subject)
				}
			},
		},
		{
			name:    "negative period rejected",
			rule:    Rule{Subject: "/", PeriodSeconds: -1, MaxRequests: int64Ptr(1)},
			wantErr: "period_seconds",
		},
		{
			name:    "windowed rule requires a limit",
			rule:    Rule{Subject: "/", PeriodSeconds: PeriodMinuteSeconds},
			wantErr: "at least one of max_requests or max_tokens",
		},
		{
			name:    "zero max_requests rejected",
			rule:    Rule{Subject: "/", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(0)},
			wantErr: "max_requests must be greater than 0",
		},
		{
			name:    "zero max_tokens rejected",
			rule:    Rule{Subject: "/", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: int64Ptr(0)},
			wantErr: "max_tokens must be greater than 0",
		},
		{
			name:    "concurrent rule rejects max_tokens",
			rule:    Rule{Subject: "/", PeriodSeconds: PeriodConcurrent, MaxRequests: int64Ptr(1), MaxTokens: int64Ptr(10)},
			wantErr: "max_tokens is not valid",
		},
		{
			name:    "concurrent rule requires max_requests",
			rule:    Rule{Subject: "/", PeriodSeconds: PeriodConcurrent},
			wantErr: "max_requests is required",
		},
		{
			name: "model subject lowercased to match case-insensitive matching",
			rule: Rule{Scope: ScopeModel, Subject: "OpenAI/GPT-4o", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(1)},
			check: func(t *testing.T, rule Rule) {
				if rule.Subject != "openai/gpt-4o" {
					t.Fatalf("subject = %q, want openai/gpt-4o", rule.Subject)
				}
			},
		},
		{
			name:    "invalid path rejected",
			rule:    Rule{Subject: "/a/../b", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: int64Ptr(1)},
			wantErr: "user path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule, err := NormalizeRule(tt.rule)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeRule() failed: %v", err)
			}
			if tt.check != nil {
				tt.check(t, rule)
			}
		})
	}
}

func TestPeriodHelpers(t *testing.T) {
	tests := []struct {
		name    string
		seconds int64
		ok      bool
	}{
		{"minute", PeriodMinuteSeconds, true},
		{"hourly", PeriodHourSeconds, true},
		{"day", PeriodDaySeconds, true},
		{"concurrent", PeriodConcurrent, true},
		{"fortnight", 0, false},
	}
	for _, tt := range tests {
		seconds, ok := PeriodSecondsFromName(tt.name)
		if ok != tt.ok || (ok && seconds != tt.seconds) {
			t.Fatalf("PeriodSecondsFromName(%q) = %d/%v, want %d/%v", tt.name, seconds, ok, tt.seconds, tt.ok)
		}
	}
	labels := map[int64]string{
		PeriodConcurrent:    "concurrent",
		PeriodMinuteSeconds: "minute",
		PeriodHourSeconds:   "hour",
		PeriodDaySeconds:    "day",
		7200:                "7200s",
	}
	for seconds, want := range labels {
		if got := PeriodLabel(seconds); got != want {
			t.Fatalf("PeriodLabel(%d) = %q, want %q", seconds, got, want)
		}
	}
}

func TestExceededErrorMessages(t *testing.T) {
	rule := Rule{Subject: "/team", PeriodSeconds: PeriodMinuteSeconds}
	tests := []struct {
		scope LimitScope
		want  string
	}{
		{ScopeRequests, "minute request limit of 5"},
		{ScopeTokens, "minute token limit of 5"},
		{ScopeConcurrency, "concurrent request limit of 5"},
	}
	for _, tt := range tests {
		err := &ExceededError{Rule: rule, Scope: tt.scope, Limit: 5}
		if !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("error %q does not contain %q", err.Error(), tt.want)
		}
		if !strings.Contains(err.Error(), "/team") {
			t.Fatalf("error %q does not name the user path", err.Error())
		}
	}
}

func TestRuleAppliesToPath(t *testing.T) {
	tests := []struct {
		rulePath    string
		requestPath string
		want        bool
	}{
		{"/", "/anything", true},
		{"/team", "/team", true},
		{"/team", "/team/app", true},
		{"/team", "/team-alpha", false},
		{"/team", "/other", false},
	}
	for _, tt := range tests {
		if got := ruleAppliesToPath(tt.rulePath, tt.requestPath); got != tt.want {
			t.Fatalf("ruleAppliesToPath(%q, %q) = %v, want %v", tt.rulePath, tt.requestPath, got, tt.want)
		}
	}
}
