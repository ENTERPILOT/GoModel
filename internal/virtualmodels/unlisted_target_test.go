package virtualmodels

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// unlistedCatalog is a fakeCatalog whose named providers serve IDs they do
// not list, as a jev provider serves pinned versions.
type unlistedCatalog struct {
	fakeCatalog
	acceptsUnlisted map[string]bool
}

func (c unlistedCatalog) AcceptsUnlistedModel(model string) bool {
	provider, _, ok := strings.Cut(model, "/")
	return ok && c.acceptsUnlisted[provider]
}

// A redirect may pin a version its provider accepts without listing it; the
// same target on a provider that lists everything it serves is still refused.
func TestService_RedirectToUnlistedModelOfAcceptingProvider(t *testing.T) {
	t.Parallel()
	catalog := unlistedCatalog{
		providers: []string{"openai", "jev"},
		supported: map[string]core.Model{
			"openai/gpt-4o":  {ID: "openai/gpt-4o"},
			"jev/jev-latest": {ID: "jev/jev-latest"},
		},
		acceptsUnlisted: map[string]bool{"jev": true},
	}
	svc, err := NewService(newSQLVMStore(t), catalog, true)
	require.NoError(t, err)
	ctx := context.Background()

	err = svc.Upsert(ctx, VirtualModel{Source: "pinned", Targets: []Target{{Model: "jev/jev-1.13.0"}}, Enabled: true})
	require.NoError(t, err)
	selector, changed, err := svc.ResolveModel(core.NewRequestedModelSelector("pinned", ""))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "jev/jev-1.13.0", selector.QualifiedModel())

	err = svc.Upsert(ctx, VirtualModel{Source: "missing", Targets: []Target{{Model: "openai/gpt-9"}}, Enabled: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "target model not found: openai/gpt-9")
}
