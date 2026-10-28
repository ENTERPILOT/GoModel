package azure

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// systemOneAnswerJSON is Microsoft-Decision-1's answer shape, as Foundry
// returns it.
const systemOneAnswerJSON = `{"model":"microsoft-decision-1","answers":{"team":{"type":"choice","choice":"engineering","probabilities":{"billing":0.05,"engineering":0.93,"support":0.02}}},"usage":{"input_tokens":90,"output_tokens":1}}`

func decide(t *testing.T, provider *Provider, body string) (*core.PassthroughResponse, error) {
	t.Helper()
	return provider.Decisions(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "decisions",
		Model:    "ms-decision-1",
		Body:     io.NopCloser(strings.NewReader(body)),
		Headers:  http.Header{"Content-Type": {"application/json"}},
	})
}

// Foundry serves Microsoft-Decision-1 at System One's path on the resource
// root, whichever Azure base URL is configured, so a Decisions request reaches
// it as System One and the answer comes back in OpenAI's shapes.
func TestDecisions_TranslatesThroughFoundrySystemOneRoute(t *testing.T) {
	for _, suffix := range []string{"/openai/v1", "/openai/deployments/gpt-4o", ""} {
		t.Run("base"+suffix, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, systemOneAnswerJSON)
			provider := newTestProvider(server.Client(), server.URL+suffix)

			resp, err := decide(t, provider, `{"model":"ms-decision-1","input":"The API returns 500 on every call.","questions":[{"type":"choice","name":"team","instructions":"Which team?","choices":[{"value":"billing","description":"Charges and refunds"},{"value":"engineering","description":"Bugs and outages"},{"value":"support","description":"How-to questions"}]}]}`)
			require.NoError(t, err)
			defer resp.Body.Close()

			sent := capture.Last(t)
			assert.Equal(t, "/providers/microsoft/v1/systemone", sent.Path)
			assert.False(t, sent.Query.Has("api-version"))
			assert.Equal(t, "test-api-key", sent.Header.Get("api-key"))
			assert.Equal(t, strconv.Itoa(len(sent.Body)), sent.Header.Get("Content-Length"), "Foundry refuses chunked bodies")
			assert.JSONEq(t, `{"model":"ms-decision-1","state":"The API returns 500 on every call.","questions":{"team":{"type":"choice","instructions":"Which team?","criteria":{"billing":"Charges and refunds","engineering":"Bugs and outages","support":"How-to questions"}}}}`, string(sent.Body))
			got, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			assert.Contains(t, string(got), `"name":"team"`)
			assert.Contains(t, string(got), `"choice":"engineering"`)
		})
	}
}

// Images are refused before the upstream: Microsoft-Decision-1 reads text and
// JSON only.
func TestDecisions_AzureRefusesImages(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{}`)
	provider := newTestProvider(server.Client(), server.URL+"/openai/v1")

	_, err := decide(t, provider, `{"model":"ms-decision-1","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}]}],"questions":[{"type":"predicate","instructions":"?"}]}`)
	providertest.AssertUnsupported(t, err)
	assert.Contains(t, err.Error(), "read text and JSON only")
	assert.Zero(t, capture.Count())
	assert.False(t, provider.DecisionsReadImages())
}

// A native System One request (POST /v1/systemone) reaches the same Foundry
// route unchanged; other passthrough endpoints keep the configured base URL.
func TestPassthrough_RoutesSystemOneToFoundry(t *testing.T) {
	tests := []struct {
		endpoint    string
		wantPath    string
		wantVersion bool
	}{
		{endpoint: "systemone", wantPath: "/providers/microsoft/v1/systemone"},
		{endpoint: "/v1/systemone", wantPath: "/providers/microsoft/v1/systemone"},
		{endpoint: "chat/completions", wantPath: "/openai/deployments/gpt-4o/chat/completions", wantVersion: true},
	}
	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, systemOneAnswerJSON)
			provider := newTestProvider(server.Client(), server.URL+"/openai/deployments/gpt-4o")
			body := `{"model":"ms-decision-1","state":"hi","questions":{"q":{"type":"noul","instructions":"?"}}}`

			resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
				Method:   http.MethodPost,
				Endpoint: tt.endpoint,
				Body:     io.NopCloser(strings.NewReader(body)),
				Headers:  http.Header{"Content-Type": {"application/json"}},
			})
			require.NoError(t, err)
			defer resp.Body.Close()

			sent := capture.Last(t)
			assert.Equal(t, tt.wantPath, sent.Path)
			assert.Equal(t, tt.wantVersion, sent.Query.Has("api-version"))
			if tt.wantPath == systemOnePath {
				assert.Equal(t, strconv.Itoa(len(sent.Body)), sent.Header.Get("Content-Length"), "Foundry refuses chunked bodies")
			}
			assert.JSONEq(t, body, string(sent.Body))
		})
	}
}
