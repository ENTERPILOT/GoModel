package providers

import "testing"

func TestSkipPassthroughHeader_ExactSkippedKeys(t *testing.T) {
	skipped := []string{
		"Authorization",
		"X-Api-Key",
		"Host",
		"Content-Length",
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
		"Cookie",
		"Forwarded",
		"Set-Cookie",
	}

	for _, key := range skipped {
		key := key
		t.Run(key, func(t *testing.T) {
			if !SkipPassthroughHeader(key) {
				t.Fatalf("expected %q to be skipped", key)
			}
		})
	}
}

func TestSkipPassthroughHeader_XForwardedPrefix(t *testing.T) {
	cases := []string{
		"X-Forwarded-For",
		"X-Forwarded-Host",
		"X-Forwarded-Proto",
		"X-Forwarded-Client-IP",
	}

	for _, key := range cases {
		key := key
		t.Run(key, func(t *testing.T) {
			if !SkipPassthroughHeader(key) {
				t.Fatalf("expected %q (X-Forwarded-* prefix) to be skipped", key)
			}
		})
	}
}

func TestSkipPassthroughHeader_NotSkipped(t *testing.T) {
	notSkipped := []string{
		"X-Request-Id",
		"Content-Type",
		"Accept",
		"User-Agent",
		"X-Stainless-Arch",
		"X-Custom",
	}

	for _, key := range notSkipped {
		key := key
		t.Run(key, func(t *testing.T) {
			if SkipPassthroughHeader(key) {
				t.Fatalf("expected %q NOT to be skipped", key)
			}
		})
	}
}

func TestSkipPassthroughHeader_CaseInsensitive(t *testing.T) {
	cases := []struct {
		input    string
		expected bool
	}{
		{"authorization", true},
		{"AUTHORIZATION", true},
		{"x-api-key", true},
		{"x-api-KEY", true},
		{"x-forwarded-for", true},
		{"X-FORWARDED-FOR", true},
		{"x-forwarded-proto", true},
		{"x-request-id", false},
		{"CONTENT-TYPE", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			got := SkipPassthroughHeader(tc.input)
			if got != tc.expected {
				t.Fatalf("SkipPassthroughHeader(%q) = %v, want %v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestSkipPassthroughHeader_TrimsWhitespace(t *testing.T) {
	if !SkipPassthroughHeader("  Authorization  ") {
		t.Fatalf("expected whitespace-padded Authorization to be skipped")
	}

	if !SkipPassthroughHeader("\tX-Forwarded-For\n") {
		t.Fatalf("expected whitespace-padded X-Forwarded-For to be skipped")
	}

	if SkipPassthroughHeader("  Content-Type  ") {
		t.Fatalf("expected whitespace-padded Content-Type NOT to be skipped")
	}
}

func TestSkipPassthroughHeader_UserExtras(t *testing.T) {
	t.Run("empty_extras_does_nothing", func(t *testing.T) {
		if SkipPassthroughHeader("X-Custom", "" /* no extras */) {
			t.Fatalf("X-Custom must not be skipped with empty extras")
		}
		if !SkipPassthroughHeader("Authorization") {
			t.Fatalf("Authorization must still be skipped regardless of extras")
		}
	})

	t.Run("exact_match", func(t *testing.T) {
		if !SkipPassthroughHeader("X-Custom", "X-Custom") {
			t.Fatalf("X-Custom must be skipped when in extras")
		}
	})

	t.Run("case_insensitive_match", func(t *testing.T) {
		if !SkipPassthroughHeader("x-custom", "X-Custom") {
			t.Fatalf("lowercase x-custom must match uppercase extra X-Custom")
		}
		if !SkipPassthroughHeader("X-CUSTOM", "x-custom") {
			t.Fatalf("uppercase X-CUSTOM must match lowercase extra x-custom")
		}
	})

	t.Run("whitespace_trimmed_match", func(t *testing.T) {
		if !SkipPassthroughHeader("X-Custom", "  X-Custom  ") {
			t.Fatalf("extra with whitespace padding must still match")
		}
	})

	t.Run("empty_extra_entry_ignored", func(t *testing.T) {
		if SkipPassthroughHeader("X-Custom", "", "  ", "Other") {
			t.Fatalf("X-Custom must not be skipped by empty/whitespace extra entries")
		}
		if !SkipPassthroughHeader("Other", "", "Other") {
			t.Fatalf("Other must be skipped when listed in extras")
		}
	})

	t.Run("does_not_affect_always_on_floor", func(t *testing.T) {
		// Always-on keys must still be skipped even if not in extras.
		if !SkipPassthroughHeader("Authorization", "X-Custom") {
			t.Fatalf("Authorization must remain skipped")
		}
		if !SkipPassthroughHeader("X-Forwarded-For", "X-Custom") {
			t.Fatalf("X-Forwarded-* prefix must remain skipped")
		}
	})
}
