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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetch_EmptyURL(t *testing.T) {
	list, raw, err := Fetch(context.Background(), "")
	assert.Nil(t, list)
	assert.Nil(t, raw)
	assert.NoError(t, err)
}

func TestFetch_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/json", r.Header.Get("Accept"))

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
	require.NoError(t, err)

	require.NotNil(t, list, "expected non-nil list")
	require.NotNil(t, raw, "expected non-nil raw bytes")
	assert.Equal(t, 1, list.Version)
	assert.Len(t, list.Providers, 1)
	assert.Len(t, list.Models, 1)
}

func TestFetch_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, _, err := Fetch(context.Background(), server.URL)
	assert.Error(t, err)
}

func TestFetch_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	_, _, err := Fetch(context.Background(), server.URL)
	assert.Error(t, err)
}

func TestFetch_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
		_, _ = w.Write([]byte("{}"))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _, err := Fetch(ctx, server.URL)
	assert.Error(t, err)
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
	assert.Error(t, err)

	if err != nil && !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected 'too large' error, got: %v", err)
	}
}

func TestFetchIfChanged_CapturesETag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("If-None-Match")
		assert.Empty(t, got)

		w.Header().Set("ETag", `"abc123"`)
		_, _ = w.Write([]byte(`{"version": 1, "providers": {}, "models": {}, "provider_models": {}}`))
	}))
	defer server.Close()

	result, err := FetchIfChanged(context.Background(), server.URL, "")
	require.NoError(t, err)
	assert.False(t, result.NotModified)
	require.NotNil(t, result.List)
	require.NotNil(t, result.Raw)
	assert.Equal(t, `"abc123"`, result.ETag)
}

func TestFetchIfChanged_NotModified(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("If-None-Match")
		assert.Equal(t, `"abc123"`, got)

		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	result, err := FetchIfChanged(context.Background(), server.URL, `"abc123"`)
	require.NoError(t, err)
	require.True(t, result.NotModified)
	assert.Nil(t, result.List)
	assert.Nil(t, result.Raw)
	assert.Equal(t, `"abc123"`, result.ETag)
}

func TestFetchIfChanged_NotModifiedAdoptsResponseETag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"refreshed"`)
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	result, err := FetchIfChanged(context.Background(), server.URL, `"stale"`)
	require.NoError(t, err)
	require.True(t, result.NotModified)
	assert.Equal(t, `"refreshed"`, result.ETag)
}

func TestFetchIfChanged_ChangedContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v2"`)
		_, _ = w.Write([]byte(`{"version": 2, "providers": {}, "models": {}, "provider_models": {}}`))
	}))
	defer server.Close()

	result, err := FetchIfChanged(context.Background(), server.URL, `"v1"`)
	require.NoError(t, err)
	assert.False(t, result.NotModified)
	require.NotNil(t, result.List)
	require.Equal(t, 2, result.List.Version)
	assert.Equal(t, `"v2"`, result.ETag)
}

func TestFetchIfChanged_ServerWithoutETagSupport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version": 1, "providers": {}, "models": {}, "provider_models": {}}`))
	}))
	defer server.Close()

	result, err := FetchIfChanged(context.Background(), server.URL, `"stale"`)
	require.NoError(t, err)
	assert.False(t, result.NotModified)
	require.NotNil(t, result.List)
	assert.Empty(t, result.ETag)
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
	require.NoError(t, err)
	assert.Equal(t, 1, list.Version)
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
	require.NoError(t, err)

	require.NotNil(t, list.providerModelByActualID, "expected providerModelByActualID to be built")
	compositeKey, ok := list.providerModelByActualID["openai/gpt-4o-2024-08-06"]
	require.True(t, ok)
	assert.Equal(t, "openai/gpt-4o", compositeKey)

	targets := list.aliasTargetsByID["gpt-4o-latest"]
	require.Len(t, targets, 2)

	var sawGeneric bool
	var sawProviderSpecific bool
	for _, target := range targets {
		require.Equal(t, "gpt-4o", target.ModelRef)

		if target.ProviderType == "" {
			sawGeneric = true
		}
		if target.ProviderType == "openai" {
			sawProviderSpecific = true
		}
	}
	require.True(t, sawGeneric)
	require.True(t, sawProviderSpecific)
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
	require.NoError(t, err)
	got := list.providerModelByActualID["openai/gpt-4o-2024-11-20"]
	require.Equal(t, "openai/gpt-4o", got)
	require.NotNil(t, list.Models["gpt-4o"].Rankings["chatbot_arena"].Elo)
}

func TestParse_InvalidJSON(t *testing.T) {
	_, err := Parse([]byte("not json"))
	assert.Error(t, err)
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
	require.NoError(t, err)

	pricing := list.ProviderModels["deepseek/deepseek-v4-flash"].Pricing
	require.NotNil(t, pricing)
	require.Len(t, pricing.TimeWindows, 1)

	window := pricing.TimeWindows[0]
	require.Equal(t, "off_peak", window.Label)
	require.Len(t, window.UTCRanges, 3, "window = %+v, want off_peak with 3 ranges", window)
	got := window.UTCRanges[0]
	require.Len(t, got.Days, 5)
	require.Equal(t, "10:00", got.Start)
	require.Equal(t, "24:00", got.End, "range[0] = %+v", got)
	got = window.UTCRanges[2]
	require.Empty(t, got.Days)
	require.Equal(t, "04:00", got.Start, "range[2] = %+v, want no day restriction", got)
	require.NotNil(t, window.Pricing.InputPerMtok)
	require.Equal(t, 0.22, *window.Pricing.InputPerMtok)
	require.Nil(t, window.Pricing.CacheWritePerMtok, "window rates = %+v", window.Pricing)

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
	err := os.WriteFile(path, content, 0o600)
	require.NoError(t, err)

	for _, location := range []string{path, "file://" + path} {
		t.Run(location, func(t *testing.T) {
			first, err := FetchIfChanged(context.Background(), location, "")
			require.NoError(t, err)
			require.NotNil(t, first.List)
			require.False(t, first.NotModified)
			require.NotEmpty(t, first.ETag, "first read should parse and return a validator: %+v", first)
			require.Len(t, first.List.Models, 1)

			second, err := FetchIfChanged(context.Background(), location, first.ETag)
			require.NoError(t, err)
			require.True(t, second.NotModified)
			require.Equal(t, first.ETag, second.ETag, "unchanged file should report NotModified with the same validator: %+v", second)
			err = os.WriteFile(path, []byte(`{"models":{}}`), 0o600)
			require.NoError(t, err)

			third, err := FetchIfChanged(context.Background(), location, first.ETag)
			require.NoError(t, err)
			require.False(t, third.NotModified)
			require.NotNil(t, third.List)
			require.NotEqual(t, first.ETag, third.ETag, "changed file should be re-read with a new validator: %+v", third)
			err = // Restore for the next location.
				os.WriteFile(path, content, 0o600)
			require.NoError(t, err)
		})
	}
}

func TestFetchIfChanged_LocalFileErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := FetchIfChanged(context.Background(), filepath.Join(dir, "missing.json"), "")
	assert.Error(t, err)

	bad := filepath.Join(dir, "bad.json")
	err = os.WriteFile(bad, []byte("not json"), 0o600)
	require.NoError(t, err)
	_, err = FetchIfChanged(context.Background(), bad, "")
	require.Error(t, err)
	_, err = FetchIfChanged(context.Background(), dir, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")
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
		assert.Equal(t, tt.wantOK, gotOK)
		assert.Equal(t, tt.wantPath, gotPath, "localPath(%q) = (%q, %v), want (%q, %v)", tt.in, gotPath, gotOK, tt.wantPath, tt.wantOK)
	}
}

func TestFetchIfChanged_LocalFileOversizedIsRejectedBeforeAllocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.json")
	f, err := os.Create(path)
	require.NoError(t, err)
	err = // A sparse file well past the limit costs no disk and no memory to
		// create; a full read would allocate the whole thing.
		f.Truncate(maxBodySize * 8)
	require.NoError(t, err)

	f.Close()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err = FetchIfChanged(context.Background(), path, "")
	runtime.ReadMemStats(&after)
	require.Error(t, err)
	require.Contains(t, err.Error(), "too large")
	allocated := after.TotalAlloc - before.TotalAlloc
	require.LessOrEqual(t, allocated, uint64(maxBodySize))
}
