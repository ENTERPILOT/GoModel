package modeldata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFetch_EmptyURL(t *testing.T) {
	list, raw, err := Fetch(context.Background(), "")
	if list != nil || raw != nil || err != nil {
		t.Error("expected all nil for empty URL")
	}
}

func TestFetch_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" {
			t.Error("expected Accept: application/json header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"version": 1,
			"updated_at": "2025-01-01T00:00:00Z",
			"providers": {"openai": {"display_name": "OpenAI"}},
			"models": {"gpt-4o": {"display_name": "GPT-4o", "modes": ["chat"]}},
			"provider_models": {}
		}`))
	}))
	defer server.Close()

	list, raw, err := Fetch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if list == nil {
		t.Fatal("expected non-nil list")
		return
	}
	if raw == nil {
		t.Fatal("expected non-nil raw bytes")
		return
	}
	if list.Version != 1 {
		t.Errorf("Version = %d, want 1", list.Version)
	}
	if len(list.Providers) != 1 {
		t.Errorf("Providers len = %d, want 1", len(list.Providers))
	}
	if len(list.Models) != 1 {
		t.Errorf("Models len = %d, want 1", len(list.Models))
	}
}

func TestFetch_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, _, err := Fetch(context.Background(), server.URL)
	if err == nil {
		t.Error("expected error for 404 response")
	}
}

func TestFetch_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	_, _, err := Fetch(context.Background(), server.URL)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestFetch_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _, err := Fetch(ctx, server.URL)
	if err == nil {
		t.Error("expected error for timeout")
	}
}

func TestFetch_OversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Write just over 10 MB
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", 10*1024*1024)))
		_, _ = w.Write([]byte(`"}`))
	}))
	defer server.Close()

	_, _, err := Fetch(context.Background(), server.URL)
	if err == nil {
		t.Error("expected error for oversized body")
	}
	if err != nil && !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected 'too large' error, got: %v", err)
	}
}

func TestFetchIfChanged_CapturesETag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("If-None-Match"); got != "" {
			t.Errorf("unexpected If-None-Match header %q on unconditional fetch", got)
		}
		w.Header().Set("ETag", `"abc123"`)
		_, _ = w.Write([]byte(`{"version": 1, "providers": {}, "models": {}, "provider_models": {}}`))
	}))
	defer server.Close()

	result, err := FetchIfChanged(context.Background(), server.URL, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NotModified {
		t.Error("expected NotModified=false for 200 response")
	}
	if result.List == nil || result.Raw == nil {
		t.Fatal("expected list and raw bytes")
	}
	if result.ETag != `"abc123"` {
		t.Errorf("ETag = %q, want %q", result.ETag, `"abc123"`)
	}
}

func TestFetchIfChanged_NotModified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("If-None-Match"); got != `"abc123"` {
			t.Errorf("If-None-Match = %q, want %q", got, `"abc123"`)
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	result, err := FetchIfChanged(context.Background(), server.URL, `"abc123"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.NotModified {
		t.Fatal("expected NotModified=true for 304 response")
	}
	if result.List != nil || result.Raw != nil {
		t.Error("expected nil list and raw on 304")
	}
	if result.ETag != `"abc123"` {
		t.Errorf("ETag = %q, want the presented validator carried forward", result.ETag)
	}
}

func TestFetchIfChanged_NotModifiedAdoptsResponseETag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"refreshed"`)
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	result, err := FetchIfChanged(context.Background(), server.URL, `"stale"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.NotModified {
		t.Fatal("expected NotModified=true for 304 response")
	}
	if result.ETag != `"refreshed"` {
		t.Errorf("ETag = %q, want the 304's refreshed validator", result.ETag)
	}
}

func TestFetchIfChanged_ChangedContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v2"`)
		_, _ = w.Write([]byte(`{"version": 2, "providers": {}, "models": {}, "provider_models": {}}`))
	}))
	defer server.Close()

	result, err := FetchIfChanged(context.Background(), server.URL, `"v1"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NotModified {
		t.Error("expected NotModified=false when content changed")
	}
	if result.List == nil || result.List.Version != 2 {
		t.Fatal("expected updated list")
	}
	if result.ETag != `"v2"` {
		t.Errorf("ETag = %q, want %q", result.ETag, `"v2"`)
	}
}

func TestFetchIfChanged_ServerWithoutETagSupport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version": 1, "providers": {}, "models": {}, "provider_models": {}}`))
	}))
	defer server.Close()

	result, err := FetchIfChanged(context.Background(), server.URL, `"stale"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.NotModified {
		t.Error("expected NotModified=false when server ignores validators")
	}
	if result.List == nil {
		t.Fatal("expected list from 200 response")
	}
	if result.ETag != "" {
		t.Errorf("ETag = %q, want empty when server returns none", result.ETag)
	}
}

func TestParse_ValidJSON(t *testing.T) {
	raw := []byte(`{
		"version": 1,
		"updated_at": "2025-01-01T00:00:00Z",
		"providers": {},
		"models": {},
		"provider_models": {}
	}`)
	list, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if list.Version != 1 {
		t.Errorf("Version = %d, want 1", list.Version)
	}
}

func TestParse_BuildsReverseIndex(t *testing.T) {
	raw := []byte(`{
		"version": 1,
		"updated_at": "2025-01-01T00:00:00Z",
		"providers": {
			"openai": {"display_name": "OpenAI"}
		},
		"models": {
			"gpt-4o": {
				"display_name": "GPT-4o",
				"modes": ["chat"],
				"aliases": ["gpt-4o-latest", "openai/gpt-4o-latest"]
			}
		},
		"provider_models": {
			"openai/gpt-4o": {
				"model_ref": "gpt-4o",
				"provider_model_id": "gpt-4o-2024-08-06",
				"enabled": true
			}
		}
	}`)
	list, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if list.providerModelByActualID == nil {
		t.Fatal("expected providerModelByActualID to be built")
		return
	}
	compositeKey, ok := list.providerModelByActualID["openai/gpt-4o-2024-08-06"]
	if !ok {
		t.Fatal("expected reverse index entry for openai/gpt-4o-2024-08-06")
	}
	if compositeKey != "openai/gpt-4o" {
		t.Errorf("reverse index = %s, want openai/gpt-4o", compositeKey)
	}
	targets := list.aliasTargetsByID["gpt-4o-latest"]
	if len(targets) != 2 {
		t.Fatalf("expected 2 alias targets for gpt-4o-latest, got %d", len(targets))
	}
	var sawGeneric bool
	var sawProviderSpecific bool
	for _, target := range targets {
		if target.ModelRef != "gpt-4o" {
			t.Fatalf("alias target ModelRef = %q, want gpt-4o", target.ModelRef)
		}
		if target.ProviderType == "" {
			sawGeneric = true
		}
		if target.ProviderType == "openai" {
			sawProviderSpecific = true
		}
	}
	if !sawGeneric {
		t.Fatal("expected generic alias target for gpt-4o-latest")
	}
	if !sawProviderSpecific {
		t.Fatal("expected provider-qualified alias target for gpt-4o-latest")
	}
}

func TestParse_BuildsReverseIndexFromProviderModelID(t *testing.T) {
	raw := []byte(`{
		"version": 1,
		"updated_at": "2025-01-01T00:00:00Z",
		"providers": {},
		"models": {
			"gpt-4o": {
				"display_name": "GPT-4o",
				"modes": ["chat"],
				"rankings": {
					"chatbot_arena": {
						"elo": 1287,
						"rank": 3,
						"as_of": "2026-02-01"
					}
				}
			}
		},
		"provider_models": {
			"openai/gpt-4o": {
				"model_ref": "gpt-4o",
				"provider_model_id": "gpt-4o-2024-11-20",
				"enabled": true
			}
		}
	}`)
	list, err := Parse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := list.providerModelByActualID["openai/gpt-4o-2024-11-20"]; got != "openai/gpt-4o" {
		t.Fatalf("reverse index = %q, want %q", got, "openai/gpt-4o")
	}
	if list.Models["gpt-4o"].Rankings["chatbot_arena"].Elo == nil {
		t.Fatal("expected elo ranking to be parsed")
	}
}

func TestParse_InvalidJSON(t *testing.T) {
	_, err := Parse([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParse_PricingTimeWindows(t *testing.T) {
	// The registry's time_windows format (ai-model-list pricing.time_windows),
	// including a per-range "days" list and an unrelated future field that an
	// older GoModel must keep ignoring.
	raw := []byte(`{
		"version": 1,
		"updated_at": "2026-08-24T00:00:00Z",
		"providers": {"deepseek": {"display_name": "DeepSeek", "api_type": "openai"}},
		"models": {"deepseek-v4-flash": {"display_name": "DeepSeek V4 Flash", "modes": ["chat"]}},
		"provider_models": {
			"deepseek/deepseek-v4-flash": {
				"model_ref": "deepseek-v4-flash",
				"enabled": true,
				"pricing": {
					"currency": "USD",
					"input_per_mtok": 0.44,
					"output_per_mtok": 1.32,
					"cached_input_per_mtok": 0.014,
					"time_windows": [{
						"label": "off_peak",
						"utc_ranges": [
							{"days": ["mon", "tue", "wed", "thu", "fri"], "start": "10:00", "end": "24:00"},
							{"days": ["sat", "sun"], "start": "00:00", "end": "24:00"},
							{"start": "04:00", "end": "06:00", "future_field": true}
						],
						"pricing": {"input_per_mtok": 0.22, "output_per_mtok": 0.66, "cached_input_per_mtok": 0.007}
					}]
				}
			}
		}
	}`)

	list, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	pricing := list.ProviderModels["deepseek/deepseek-v4-flash"].Pricing
	if pricing == nil || len(pricing.TimeWindows) != 1 {
		t.Fatalf("pricing = %+v, want one time window", pricing)
	}
	window := pricing.TimeWindows[0]
	if window.Label != "off_peak" || len(window.UTCRanges) != 3 {
		t.Fatalf("window = %+v, want off_peak with 3 ranges", window)
	}
	if got := window.UTCRanges[0]; len(got.Days) != 5 || got.Start != "10:00" || got.End != "24:00" {
		t.Fatalf("range[0] = %+v", got)
	}
	if got := window.UTCRanges[2]; len(got.Days) != 0 || got.Start != "04:00" {
		t.Fatalf("range[2] = %+v, want no day restriction", got)
	}
	if window.Pricing.InputPerMtok == nil || *window.Pricing.InputPerMtok != 0.22 || window.Pricing.CacheWritePerMtok != nil {
		t.Fatalf("window rates = %+v", window.Pricing)
	}

	// Saturday 08:00 UTC would be a peak hour on a weekday.
	saturday := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	if got := pricing.AtTime(saturday); *got.InputPerMtok != 0.22 || *got.OutputPerMtok != 0.66 {
		t.Fatalf("AtTime(saturday) = %v / %v, want off-peak 0.22 / 0.66", *got.InputPerMtok, *got.OutputPerMtok)
	}
	monday := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	if got := pricing.AtTime(monday); *got.InputPerMtok != 0.44 {
		t.Fatalf("AtTime(monday peak) = %v, want base 0.44", *got.InputPerMtok)
	}
}

func TestFetchIfChanged_LocalFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	content := []byte(`{"models":{"openai/gpt-4o":{"provider":"openai","name":"gpt-4o"}}}`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, location := range []string{path, "file://" + path} {
		t.Run(location, func(t *testing.T) {
			first, err := FetchIfChanged(context.Background(), location, "")
			if err != nil {
				t.Fatalf("first read: %v", err)
			}
			if first.List == nil || first.NotModified || first.ETag == "" {
				t.Fatalf("first read should parse and return a validator: %+v", first)
			}
			if len(first.List.Models) != 1 {
				t.Fatalf("models = %d, want 1", len(first.List.Models))
			}

			second, err := FetchIfChanged(context.Background(), location, first.ETag)
			if err != nil {
				t.Fatalf("second read: %v", err)
			}
			if !second.NotModified || second.ETag != first.ETag {
				t.Fatalf("unchanged file should report NotModified with the same validator: %+v", second)
			}

			if err := os.WriteFile(path, []byte(`{"models":{}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			third, err := FetchIfChanged(context.Background(), location, first.ETag)
			if err != nil {
				t.Fatalf("third read: %v", err)
			}
			if third.NotModified || third.List == nil || third.ETag == first.ETag {
				t.Fatalf("changed file should be re-read with a new validator: %+v", third)
			}
			// Restore for the next location.
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFetchIfChanged_LocalFileErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := FetchIfChanged(context.Background(), filepath.Join(dir, "missing.json"), ""); err == nil {
		t.Error("missing file should error")
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FetchIfChanged(context.Background(), bad, ""); err == nil {
		t.Error("invalid JSON should error")
	}
}

func TestLocalPath(t *testing.T) {
	tests := []struct {
		in       string
		wantPath string
		wantOK   bool
	}{
		{"https://example.com/m.json", "", false},
		{"http://example.com/m.json", "", false},
		{"file:///etc/gomodel/models.json", "/etc/gomodel/models.json", true},
		{"file://localhost/etc/models.json", "/etc/models.json", true},
		{"/etc/gomodel/models.json", "/etc/gomodel/models.json", true},
		{"./models.json", "./models.json", true},
		{"  /tmp/m.json  ", "/tmp/m.json", true},
	}
	for _, tt := range tests {
		gotPath, gotOK := localPath(tt.in)
		if gotOK != tt.wantOK || gotPath != tt.wantPath {
			t.Errorf("localPath(%q) = (%q, %v), want (%q, %v)", tt.in, gotPath, gotOK, tt.wantPath, tt.wantOK)
		}
	}
}

func TestFetchIfChanged_LocalFileOversizedIsRejectedBeforeAllocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// A sparse file well past the limit costs no disk and no memory to
	// create; a full read would allocate the whole thing.
	if err := f.Truncate(maxBodySize * 8); err != nil {
		t.Fatal(err)
	}
	f.Close()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err = FetchIfChanged(context.Background(), path, "")
	runtime.ReadMemStats(&after)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected size rejection, got %v", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > maxBodySize {
		t.Fatalf("oversized file allocated %d bytes before rejection; must stay under %d", allocated, maxBodySize)
	}
}
