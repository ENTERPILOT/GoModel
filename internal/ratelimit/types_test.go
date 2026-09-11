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
			rule: Rule{Subject: "team/alpha/", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(10))},
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
			rule: Rule{Subject: "", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: new(int64(100))},
			check: func(t *testing.T, rule Rule) {
				if rule.Subject != "/" {
					t.Fatalf("user path = %q, want /", rule.Subject)
				}
			},
		},
		{
			name:    "negative period rejected",
			rule:    Rule{Subject: "/", PeriodSeconds: -1, MaxRequests: new(int64(1))},
			wantErr: "period_seconds",
		},
		{
			name:    "windowed rule requires a limit",
			rule:    Rule{Subject: "/", PeriodSeconds: PeriodMinuteSeconds},
			wantErr: "at least one of max_requests or max_tokens",
		},
		{
			name:    "zero max_requests rejected",
			rule:    Rule{Subject: "/", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(0))},
			wantErr: "max_requests must be greater than 0",
		},
		{
			name:    "zero max_tokens rejected",
			rule:    Rule{Subject: "/", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: new(int64(0))},
			wantErr: "max_tokens must be greater than 0",
		},
		{
			name:    "concurrent rule rejects max_tokens",
			rule:    Rule{Subject: "/", PeriodSeconds: PeriodConcurrent, MaxRequests: new(int64(1)), MaxTokens: new(int64(10))},
			wantErr: "max_tokens is not valid",
		},
		{
			name:    "concurrent rule requires max_requests",
			rule:    Rule{Subject: "/", PeriodSeconds: PeriodConcurrent},
			wantErr: "max_requests is required",
		},
		{
			name: "model subject lowercased to match case-insensitive matching",
			rule: Rule{Scope: ScopeModel, Subject: "OpenAI/GPT-4o", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(1))},
			check: func(t *testing.T, rule Rule) {
				if rule.Subject != "openai/gpt-4o" {
					t.Fatalf("subject = %q, want openai/gpt-4o", rule.Subject)
				}
			},
		},
		{
			name:    "invalid path rejected",
			rule:    Rule{Subject: "/a/../b", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(1))},
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

// Provider and model subjects are case-folded to stay the match key, but the
// spelling the rule was written with is what clients and logs must see: the
// folded form can name no configured provider at all.
func TestRuleDisplaySubject(t *testing.T) {
	tests := []struct {
		name        string
		rule        Rule
		wantSubject string
		wantDisplay string
		wantLabel   string
	}{
		{
			name:        "provider keeps the written spelling",
			rule:        Rule{Scope: ScopeProvider, Subject: "mockA", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(1))},
			wantSubject: "mocka",
			wantDisplay: "mockA",
			wantLabel:   "provider mockA",
		},
		{
			name:        "model keeps the written spelling",
			rule:        Rule{Scope: ScopeModel, Subject: "OpenAI/GPT-4o", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: new(int64(10))},
			wantSubject: "openai/gpt-4o",
			wantDisplay: "OpenAI/GPT-4o",
			wantLabel:   "model OpenAI/GPT-4o",
		},
		{
			name:        "already folded stores no display form",
			rule:        Rule{Scope: ScopeProvider, Subject: "openai", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(1))},
			wantSubject: "openai",
			wantDisplay: "",
			wantLabel:   "provider openai",
		},
		{
			name:        "user path is not folded",
			rule:        Rule{Scope: ScopeUserPath, Subject: "/Team/Alpha", PeriodSeconds: PeriodMinuteSeconds, MaxRequests: new(int64(1))},
			wantSubject: "/Team/Alpha",
			wantDisplay: "",
			wantLabel:   "/Team/Alpha",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized, err := NormalizeRule(tt.rule)
			if err != nil {
				t.Fatalf("NormalizeRule() failed: %v", err)
			}
			if normalized.Subject != tt.wantSubject {
				t.Fatalf("Subject = %q, want %q", normalized.Subject, tt.wantSubject)
			}
			if normalized.SubjectDisplay != tt.wantDisplay {
				t.Fatalf("SubjectDisplay = %q, want %q", normalized.SubjectDisplay, tt.wantDisplay)
			}
			if got := normalized.SubjectLabel(); got != tt.wantLabel {
				t.Fatalf("SubjectLabel() = %q, want %q", got, tt.wantLabel)
			}
			breach := &ExceededError{Rule: normalized, Scope: ScopeRequests, Limit: 1}
			if !strings.Contains(breach.Error(), tt.wantLabel) {
				t.Fatalf("breach message %q does not name %q", breach.Error(), tt.wantLabel)
			}
		})
	}
}

// Rules stored before the display form existed carry none; they degrade to
// the folded subject instead of reporting nothing.
func TestRuleDisplaySubjectFallsBackToStoredSubject(t *testing.T) {
	rule := Rule{Scope: ScopeProvider, Subject: "mocka"}
	if got := rule.DisplaySubject(); got != "mocka" {
		t.Fatalf("DisplaySubject() = %q, want %q", got, "mocka")
	}
	if got := rule.SubjectLabel(); got != "provider mocka" {
		t.Fatalf("SubjectLabel() = %q, want %q", got, "provider mocka")
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
