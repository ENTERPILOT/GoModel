package providers

import (
	"sync"
	"testing"
)

func TestNewKeyring(t *testing.T) {
	tests := []struct {
		name string
		keys []string
		want []string
	}{
		{name: "no keys", keys: nil},
		{name: "only empty keys", keys: []string{"", ""}},
		{name: "single key", keys: []string{"k1"}, want: []string{"k1"}},
		{name: "preserves order", keys: []string{"k1", "k2", "k3"}, want: []string{"k1", "k2", "k3"}},
		{name: "drops empty keys", keys: []string{"k1", "", "k2"}, want: []string{"k1", "k2"}},
		{name: "drops duplicates", keys: []string{"k1", "k2", "k1"}, want: []string{"k1", "k2"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ring := NewKeyring(tt.keys...)

			if len(tt.want) == 0 {
				if ring != nil {
					t.Fatalf("NewKeyring(%q) = %v, want nil for an unusable key set", tt.keys, ring.keys)
				}
				return
			}
			if ring.Len() != len(tt.want) {
				t.Fatalf("Len() = %d, want %d", ring.Len(), len(tt.want))
			}
			// One full cycle reproduces the configured order.
			for i, want := range tt.want {
				if got := ring.Next(); got != want {
					t.Errorf("Next() call %d = %q, want %q", i+1, got, want)
				}
			}
		})
	}
}

func TestKeyringNextCyclesRoundRobin(t *testing.T) {
	ring := NewKeyring("k1", "k2", "k3")

	// Two full cycles: the ring must wrap, not run dry.
	want := []string{"k1", "k2", "k3", "k1", "k2", "k3"}
	for i, expected := range want {
		if got := ring.Next(); got != expected {
			t.Errorf("Next() call %d = %q, want %q", i+1, got, expected)
		}
	}
}

// A single key must behave exactly as it did before rotation existed: every
// request presents the same credential, so provider prompt caching still works.
func TestKeyringSingleKeyNeverRotates(t *testing.T) {
	ring := NewKeyring("only")

	if ring.Rotates() {
		t.Error("Rotates() = true, want false for one key")
	}
	for i := range 3 {
		if got := ring.Next(); got != "only" {
			t.Errorf("Next() call %d = %q, want %q", i+1, got, "only")
		}
	}
}

// Keyless providers (Ollama, vLLM) and constructors invoked outside the factory
// hold a nil ring; every method must stay safe.
func TestKeyringNilIsEmpty(t *testing.T) {
	var ring *Keyring

	if got := ring.Next(); got != "" {
		t.Errorf("Next() = %q, want empty", got)
	}
	if got := ring.Primary(); got != "" {
		t.Errorf("Primary() = %q, want empty", got)
	}
	if got := ring.Len(); got != 0 {
		t.Errorf("Len() = %d, want 0", got)
	}
	if ring.Rotates() {
		t.Error("Rotates() = true, want false")
	}
}

func TestKeyringPrimaryDoesNotAdvance(t *testing.T) {
	ring := NewKeyring("k1", "k2")

	for range 3 {
		if got := ring.Primary(); got != "k1" {
			t.Fatalf("Primary() = %q, want k1", got)
		}
	}
	if got := ring.Next(); got != "k1" {
		t.Errorf("Next() = %q, want k1: Primary must not consume a slot", got)
	}
}

// Providers are shared across concurrent requests, so the rotation must both be
// race-free and hand out each key an equal number of times.
func TestKeyringNextIsConcurrentAndEven(t *testing.T) {
	ring := NewKeyring("k1", "k2", "k3")

	const perKey = 200
	total := perKey * ring.Len()

	var mu sync.Mutex
	counts := make(map[string]int, ring.Len())

	var wg sync.WaitGroup
	for range total {
		wg.Go(func() {
			key := ring.Next()
			mu.Lock()
			counts[key]++
			mu.Unlock()
		})
	}
	wg.Wait()

	for _, key := range []string{"k1", "k2", "k3"} {
		if counts[key] != perKey {
			t.Errorf("key %q used %d times, want %d", key, counts[key], perKey)
		}
	}
}

func TestProviderOptionsKeyringFallsBackToStaticKey(t *testing.T) {
	// Constructed outside the factory: no ring supplied, so the single
	// constructor key is used.
	opts := ProviderOptions{}
	if got := opts.Keyring("sk-static").Next(); got != "sk-static" {
		t.Errorf("Next() = %q, want sk-static", got)
	}

	// Built by the factory: the configured ring wins over the primary key that
	// the provider constructor happens to pass along.
	opts = ProviderOptions{Keys: NewKeyring("k1", "k2")}
	ring := opts.Keyring("k1")
	if !ring.Rotates() {
		t.Fatal("Rotates() = false, want true")
	}
	if got, want := []string{ring.Next(), ring.Next(), ring.Next()}, []string{"k1", "k2", "k1"}; !equalStrings(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
