package jev

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// The hosted catalog lists aliases by name with a description and release
// date; versioned IDs are accepted by the model field without being listed.
const typesafeModelsJSON = `{
  "models": [
    {"name": "jev-latest", "description": "The most recent stable release.", "release_date": "2026-08-12"},
    {"name": "jev-preview", "description": "The most recent release, official or not.", "release_date": "2026-08-12T00:00:00Z"}
  ]
}`

// A Kev server names its checkpoint and lists the aliases it answers to,
// alongside serving details the gateway has no use for.
const kevModelsJSON = `{
  "models": [
    {"id": "kev-latest", "aliases": ["jev-latest"], "run": "jaredpalmer/kev-4b", "base": "Qwen/Qwen3.5-4B-Base",
     "device": "mps", "temperature": 2.3, "prefix_cache": {"size": 4, "hits": 0}}
  ]
}`

func TestListModels_ReadsTheHostedCatalog(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, typesafeModelsJSON)
	provider := newTestProvider("ts-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, http.MethodGet, capture.Last(t).Method)
	assert.Equal(t, "/v1/models", capture.Last(t).Path)
	assert.Equal(t, "Bearer ts-key", capture.Last(t).Header.Get("Authorization"))

	assert.Equal(t, "list", resp.Object)
	require.Len(t, resp.Data, 2)

	latest := resp.Data[0]
	assert.Equal(t, "jev-latest", latest.ID)
	assert.Equal(t, "model", latest.Object)
	assert.Equal(t, time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC).Unix(), latest.Created)
	require.NotNil(t, latest.Metadata)
	assert.Equal(t, "The most recent stable release.", latest.Metadata.Description)
	assert.Equal(t, []core.ModelCategory{core.CategoryUtility}, latest.Metadata.Categories)
	assert.Empty(t, latest.Metadata.Modes, "a decision model claims no generation mode")

	preview := resp.Data[1]
	assert.Equal(t, "jev-preview", preview.ID)
	assert.Equal(t, time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC).Unix(), preview.Created, "RFC 3339 release dates are read too")
}

// A Kev checkpoint and each alias it answers to are listed as models of
// their own, so a request naming jev-latest resolves against the catalog.
func TestListModels_ListsKevAliasesAsModels(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, kevModelsJSON)
	provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, resp.Data, 2)
	assert.Equal(t, "kev-latest", resp.Data[0].ID)
	assert.Equal(t, "jev-latest", resp.Data[1].ID)
	for _, model := range resp.Data {
		assert.Equal(t, "model", model.Object)
		assert.Zero(t, model.Created, "no release date is reported")
		require.NotNil(t, model.Metadata)
		assert.Equal(t, []core.ModelCategory{core.CategoryUtility}, model.Metadata.Categories)
	}
}

func TestListModels_SkipsNamelessAndDuplicateEntries(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"models":[
		{"description": "no name"},
		{"name": " jev-latest ", "aliases": [" ", "jev-latest", "jev-preview"]},
		{"id": "jev-preview"}
	]}`)
	provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, resp.Data, 2)
	assert.Equal(t, "jev-latest", resp.Data[0].ID)
	assert.Equal(t, "jev-preview", resp.Data[1].ID)
}

func TestListModels_PropagatesUpstreamFailure(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusUnauthorized, `{"error":"Missing or invalid API key"}`)
	provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

	_, err := provider.ListModels(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Missing or invalid API key")
}
