package ratelimit

import (
	"testing"

	"github.com/stretchr/testify/require"
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
				require.Equal(t, "/team/alpha", rule.Subject)
				require.False(t, rule.CreatedAt.IsZero())
				require.False(t, rule.UpdatedAt.IsZero())
			},
		},
		{
			name: "empty path becomes root",
			rule: Rule{Subject: "", PeriodSeconds: PeriodMinuteSeconds, MaxTokens: new(int64(100))},
			check: func(t *testing.T, rule Rule) {
				require.Equal(t, "/", rule.Subject)
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
				require.Equal(t, "openai/gpt-4o", rule.Subject)
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
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)

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
		require.Equal(t, tt.ok, ok, "PeriodSecondsFromName(%q)", tt.name)
		if ok {
			require.Equal(t, tt.seconds, seconds, "PeriodSecondsFromName(%q)", tt.name)
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
		got := PeriodLabel(seconds)
		require.Equal(t, want, got)
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
		require.ErrorContains(t, err, tt.want)
		require.ErrorContains(t, err, "/team")
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
		got := ruleAppliesToPath(tt.rulePath, tt.requestPath)
		require.Equal(t, tt.want, got, "ruleAppliesToPath(%q, %q)", tt.rulePath, tt.requestPath)
	}
}
