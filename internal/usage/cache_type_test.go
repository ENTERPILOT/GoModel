package usage

import "testing"

func TestNormalizedUsageEntryForStorageClearsInvalidCacheTypeWithoutMutatingInput(t *testing.T) {
	entry := &UsageEntry{
		ID:        "usage-1",
		RequestID: "req-1",
		CacheType: "invalid-cache-type",
	}

	got := normalizedUsageEntryForStorage(entry)
	if got == entry {
		t.Fatal("expected invalid cache type to clone entry for normalization")
	}
	if got.CacheType != "" {
		t.Fatalf("normalized CacheType = %q, want empty", got.CacheType)
	}
	if entry.CacheType != "invalid-cache-type" {
		t.Fatalf("input CacheType mutated to %q", entry.CacheType)
	}
}

func TestNormalizedUsageEntryForStorageUserPathFallbackAndCloneBehavior(t *testing.T) {
	tests := []struct {
		name             string
		entry            UsageEntry
		wantUserPath     string
		wantCacheType    string
		wantProviderName string
		wantSamePointer  bool
	}{
		{
			name:         "trims whitespace and prepends slash",
			entry:        UsageEntry{ID: "usage-0", UserPath: " team/alpha "},
			wantUserPath: "/team/alpha",
		},
		{
			name:             "invalid dotdot path falls back to root",
			entry:            UsageEntry{ID: "usage-1", UserPath: "/team/../alpha", CacheType: CacheTypeExact, ProviderName: "openai"},
			wantUserPath:     "/",
			wantCacheType:    CacheTypeExact,
			wantProviderName: "openai",
		},
		{
			name:             "invalid colon path falls back to root",
			entry:            UsageEntry{ID: "usage-2", UserPath: "/team:alpha", CacheType: CacheTypeSemantic, ProviderName: "openai"},
			wantUserPath:     "/",
			wantCacheType:    CacheTypeSemantic,
			wantProviderName: "openai",
		},
		{
			name:             "blank path falls back to root",
			entry:            UsageEntry{ID: "usage-3", UserPath: "   ", ProviderName: "openai"},
			wantUserPath:     "/",
			wantProviderName: "openai",
		},
		{
			name:             "canonical exact cache entry is reused",
			entry:            UsageEntry{ID: "usage-4", UserPath: "/team/alpha", CacheType: CacheTypeExact, ProviderName: "openai"},
			wantUserPath:     "/team/alpha",
			wantCacheType:    CacheTypeExact,
			wantProviderName: "openai",
			wantSamePointer:  true,
		},
		{
			name:            "canonical semantic cache entry with empty provider is reused",
			entry:           UsageEntry{ID: "usage-5", UserPath: "/", CacheType: CacheTypeSemantic},
			wantUserPath:    "/",
			wantCacheType:   CacheTypeSemantic,
			wantSamePointer: true,
		},
		{
			name:             "cache type normalization clones entry",
			entry:            UsageEntry{ID: "usage-6", UserPath: "/team", CacheType: "EXACT", ProviderName: "openai"},
			wantUserPath:     "/team",
			wantCacheType:    CacheTypeExact,
			wantProviderName: "openai",
		},
		{
			name:             "provider trim clones entry",
			entry:            UsageEntry{ID: "usage-7", UserPath: "/team", CacheType: CacheTypeExact, ProviderName: " openai "},
			wantUserPath:     "/team",
			wantCacheType:    CacheTypeExact,
			wantProviderName: "openai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := tt.entry
			original := entry

			got := normalizedUsageEntryForStorage(&entry)

			if same := got == &entry; same != tt.wantSamePointer {
				t.Fatalf("same pointer = %v, want %v", same, tt.wantSamePointer)
			}
			if got.UserPath != tt.wantUserPath {
				t.Fatalf("UserPath = %q, want %q", got.UserPath, tt.wantUserPath)
			}
			if got.CacheType != tt.wantCacheType {
				t.Fatalf("CacheType = %q, want %q", got.CacheType, tt.wantCacheType)
			}
			if got.ProviderName != tt.wantProviderName {
				t.Fatalf("ProviderName = %q, want %q", got.ProviderName, tt.wantProviderName)
			}
			if entry.UserPath != original.UserPath || entry.CacheType != original.CacheType || entry.ProviderName != original.ProviderName {
				t.Fatalf("input mutated from %+v to %+v", original, entry)
			}
		})
	}
}
