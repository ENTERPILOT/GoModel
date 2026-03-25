package modeldata

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	}
	if raw == nil {
		t.Fatal("expected non-nil raw bytes")
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

func TestParse_InvalidJSON(t *testing.T) {
	_, err := Parse([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}
