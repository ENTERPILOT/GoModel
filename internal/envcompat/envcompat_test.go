package envcompat

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// testVars are cleared in both spellings before each case so an ambient value
// in the developer's shell cannot make an "unset" assertion pass or fail for
// the wrong reason.
var testVars = []string{
	"SQLITE_PATH", "STORAGE_TYPE", "PORT", "REDIS_URL", "MASTER_KEY",
	"SET_RATE_LIMIT_TEAM", "SET_RATE_LIMIT_TEAM_A", "SET_RATE_LIMIT_TEAM_B",
	"SET_RATE_LIMIT_A", "SET_RATE_LIMIT_B", "SET_RATE_LIMIT_C",
	"SET_BUDGET_TEAM", "TAGGING_HEADER_1",
}

// captureWarnings installs a slog handler that records output for the duration
// of the test, clears the warn-once state, and unsets the variables the suite
// asserts on so each case starts from a known environment.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()

	for _, name := range testVars {
		unset(t, name)
		unset(t, Prefix+name)
	}

	buf := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previous)
		warned.Clear()
	})
	warned.Clear()
	return buf
}

// unset removes name for the duration of the test, restoring any prior value.
func unset(t *testing.T, name string) {
	t.Helper()
	previous, existed := os.LookupEnv(name)
	if !existed {
		return
	}
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset %s: %v", name, err)
	}
	t.Cleanup(func() {
		if err := os.Setenv(name, previous); err != nil {
			t.Fatalf("restore %s: %v", name, err)
		}
	})
}

func TestLookup(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		lookup    string
		want      string
		wantOK    bool
		wantWarn  bool
		warnMatch string
	}{
		{
			name:   "canonical spelling resolves",
			env:    map[string]string{"GOMODEL_SQLITE_PATH": "/canonical.db"},
			lookup: "SQLITE_PATH",
			want:   "/canonical.db",
			wantOK: true,
		},
		{
			name:      "legacy spelling resolves and warns",
			env:       map[string]string{"SQLITE_PATH": "/legacy.db"},
			lookup:    "SQLITE_PATH",
			want:      "/legacy.db",
			wantOK:    true,
			wantWarn:  true,
			warnMatch: "GOMODEL_SQLITE_PATH",
		},
		{
			name: "canonical wins over legacy without warning",
			env: map[string]string{
				"GOMODEL_SQLITE_PATH": "/canonical.db",
				"SQLITE_PATH":         "/legacy.db",
			},
			lookup: "SQLITE_PATH",
			want:   "/canonical.db",
			wantOK: true,
		},
		{
			name:   "unset resolves to not-ok",
			lookup: "SQLITE_PATH",
			want:   "",
			wantOK: false,
		},
		{
			name:   "empty string is distinguished from unset",
			env:    map[string]string{"GOMODEL_SQLITE_PATH": ""},
			lookup: "SQLITE_PATH",
			want:   "",
			wantOK: true,
		},
		{
			name:   "already-prefixed name is read as given",
			env:    map[string]string{"GOMODEL_MASTER_KEY": "sk-test"},
			lookup: "GOMODEL_MASTER_KEY",
			want:   "sk-test",
			wantOK: true,
		},
		{
			name:   "already-prefixed name is not double-prefixed",
			env:    map[string]string{"GOMODEL_GOMODEL_MASTER_KEY": "wrong"},
			lookup: "GOMODEL_MASTER_KEY",
			want:   "",
			wantOK: false,
		},
		{
			name:   "exempt PORT reads bare without warning",
			env:    map[string]string{"PORT": "9000"},
			lookup: "PORT",
			want:   "9000",
			wantOK: true,
		},
		{
			name:   "exempt PORT ignores the prefixed spelling",
			env:    map[string]string{"GOMODEL_PORT": "9000"},
			lookup: "PORT",
			want:   "",
			wantOK: false,
		},
		{
			name:   "exempt REDIS_URL reads bare without warning",
			env:    map[string]string{"REDIS_URL": "redis://localhost:6379"},
			lookup: "REDIS_URL",
			want:   "redis://localhost:6379",
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := captureWarnings(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			got, ok := Lookup(tt.lookup)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("Lookup(%q) = (%q, %v), want (%q, %v)", tt.lookup, got, ok, tt.want, tt.wantOK)
			}

			warnedNow := strings.Contains(buf.String(), "deprecated environment variable")
			if warnedNow != tt.wantWarn {
				t.Errorf("warning emitted = %v, want %v (log: %q)", warnedNow, tt.wantWarn, buf.String())
			}
			if tt.warnMatch != "" && !strings.Contains(buf.String(), tt.warnMatch) {
				t.Errorf("warning does not name %q; log: %q", tt.warnMatch, buf.String())
			}
		})
	}
}

func TestWarnOncePerVariable(t *testing.T) {
	buf := captureWarnings(t)
	t.Setenv("SQLITE_PATH", "/legacy.db")
	t.Setenv("STORAGE_TYPE", "sqlite")

	for range 3 {
		Get("SQLITE_PATH")
		Get("STORAGE_TYPE")
	}

	if got := strings.Count(buf.String(), `variable=SQLITE_PATH`); got != 1 {
		t.Errorf("SQLITE_PATH warned %d times, want 1", got)
	}
	if got := strings.Count(buf.String(), `variable=STORAGE_TYPE`); got != 1 {
		t.Errorf("STORAGE_TYPE warned %d times, want 1", got)
	}
}

func TestScan(t *testing.T) {
	t.Run("both spellings are collected", func(t *testing.T) {
		captureWarnings(t)
		t.Setenv("GOMODEL_SET_RATE_LIMIT_TEAM_A", "rpm=10")
		t.Setenv("SET_RATE_LIMIT_TEAM_B", "rpm=20")

		got := map[string]string{}
		for _, e := range Scan("SET_RATE_LIMIT_") {
			got[e.Suffix] = e.Value
		}

		want := map[string]string{"TEAM_A": "rpm=10", "TEAM_B": "rpm=20"}
		if len(got) != len(want) {
			t.Fatalf("Scan returned %v, want %v", got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("suffix %q = %q, want %q", k, got[k], v)
			}
		}
	})

	t.Run("canonical wins over legacy for the same suffix", func(t *testing.T) {
		captureWarnings(t)
		t.Setenv("GOMODEL_SET_RATE_LIMIT_TEAM", "rpm=10")
		t.Setenv("SET_RATE_LIMIT_TEAM", "rpm=999")

		entries := Scan("SET_RATE_LIMIT_")
		if len(entries) != 1 {
			t.Fatalf("Scan returned %d entries, want 1: %+v", len(entries), entries)
		}
		if entries[0].Value != "rpm=10" {
			t.Errorf("value = %q, want %q (canonical must win)", entries[0].Value, "rpm=10")
		}
	})

	t.Run("Name is always the legacy spelling so companions resolve", func(t *testing.T) {
		captureWarnings(t)
		t.Setenv("GOMODEL_TAGGING_HEADER_1", "X-Team")

		entries := Scan("TAGGING_HEADER_")
		if len(entries) != 1 {
			t.Fatalf("Scan returned %d entries, want 1", len(entries))
		}
		if entries[0].Name != "TAGGING_HEADER_1" {
			t.Errorf("Name = %q, want %q", entries[0].Name, "TAGGING_HEADER_1")
		}
	})

	t.Run("entries are sorted by suffix", func(t *testing.T) {
		captureWarnings(t)
		t.Setenv("SET_RATE_LIMIT_C", "rpm=3")
		t.Setenv("GOMODEL_SET_RATE_LIMIT_A", "rpm=1")
		t.Setenv("SET_RATE_LIMIT_B", "rpm=2")

		var suffixes []string
		for _, e := range Scan("SET_RATE_LIMIT_") {
			suffixes = append(suffixes, e.Suffix)
		}
		want := []string{"A", "B", "C"}
		if len(suffixes) != len(want) {
			t.Fatalf("suffixes = %v, want %v", suffixes, want)
		}
		for i := range want {
			if suffixes[i] != want[i] {
				t.Fatalf("suffixes = %v, want %v", suffixes, want)
			}
		}
	})

	t.Run("legacy entry warns", func(t *testing.T) {
		buf := captureWarnings(t)
		t.Setenv("SET_BUDGET_TEAM", "100")

		Scan("SET_BUDGET_")

		if !strings.Contains(buf.String(), "variable=SET_BUDGET_TEAM") {
			t.Errorf("expected deprecation warning naming SET_BUDGET_TEAM; log: %q", buf.String())
		}
		if !strings.Contains(buf.String(), "use=GOMODEL_SET_BUDGET_TEAM") {
			t.Errorf("expected warning to name the canonical spelling; log: %q", buf.String())
		}
	})
}

// An empty canonical value must not shadow a working legacy value: a compose
// file with an unexpanded GOMODEL_X= would otherwise silently discard the
// deployment's real X.
func TestEmptyCanonicalDoesNotShadowLegacy(t *testing.T) {
	captureWarnings(t)
	t.Setenv("GOMODEL_SQLITE_PATH", "")
	t.Setenv("SQLITE_PATH", "/legacy.db")

	got, ok := Lookup("SQLITE_PATH")
	if got != "/legacy.db" || !ok {
		t.Errorf("Lookup = (%q, %v), want (%q, true)", got, ok, "/legacy.db")
	}
}

func TestEmptyCanonicalReportsPresenceWhenNoLegacy(t *testing.T) {
	captureWarnings(t)
	t.Setenv("GOMODEL_SQLITE_PATH", "")

	got, ok := Lookup("SQLITE_PATH")
	if got != "" || !ok {
		t.Errorf("Lookup = (%q, %v), want (%q, true)", got, ok, "")
	}
}
