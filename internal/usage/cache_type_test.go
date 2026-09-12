package usage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizedUsageEntryForStorageClearsInvalidCacheTypeWithoutMutatingInput(t *testing.T) {
	entry := &UsageEntry{
		ID:        "usage-1",
		RequestID: "req-1",
		CacheType: "invalid-cache-type",
	}

	got := normalizedUsageEntryForStorage(entry)
	require.NotEqual(t, entry, got)
	require.Empty(t, got.CacheType)
	require.Equal(t, "invalid-cache-type", entry.CacheType)
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
			same := got == &entry
			require.Equal(t, tt.wantSamePointer, same)
			require.Equal(t, tt.wantUserPath, got.UserPath)
			require.Equal(t, tt.wantCacheType, got.CacheType)
			require.Equal(t, tt.wantProviderName, got.ProviderName)
			require.Equal(t, original.UserPath, entry.UserPath)
			require.Equal(t, original.CacheType, entry.CacheType)
			require.Equal(t, original.ProviderName, entry.ProviderName, "input mutated from %+v to %+v", original, entry)
		})
	}
}
