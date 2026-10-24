//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/tests/integration/dbassert"
)

func TestAdminUsageByEndpoint_PostgreSQL(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "postgresql",
		UsageEnabled:          true,
		AdminEndpointsEnabled: true,
	})
	dbassert.ClearUsage(t, fixture.PgPool)

	assertUsageByEndpoint(t, fixture)
}

func TestAdminUsageByEndpoint_MongoDB(t *testing.T) {
	fixture := SetupTestServer(t, TestServerConfig{
		DBType:                "mongodb",
		UsageEnabled:          true,
		AdminEndpointsEnabled: true,
	})
	dbassert.ClearUsageMongo(t, fixture.MongoDb)

	assertUsageByEndpoint(t, fixture)
}

// assertUsageByEndpoint sends two chat requests and one Responses request,
// then checks the endpoint breakdown and the operation/endpoint filters
// against the storage backend's real queries.
func assertUsageByEndpoint(t *testing.T, fixture *TestServerFixture) {
	t.Helper()
	for range 2 {
		resp := sendChatRequest(t, fixture.ServerURL, newChatRequest("gpt-4", "Hello!"))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		closeBody(resp)
	}
	resp := sendResponsesRequestWithHeaders(t, fixture.ServerURL, newResponsesRequest("gpt-4", "Hello!"), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	closeBody(resp)
	waitForUsageFlush(t, fixture.ServerURL, 3)

	rows := getAdminJSON[[]usage.EndpointUsage](t, fixture.ServerURL+"/admin/usage/endpoints?days=30")
	byEndpoint := map[string]usage.EndpointUsage{}
	for _, row := range rows {
		byEndpoint[row.Endpoint] = row
	}
	require.Len(t, byEndpoint, 2)
	assert.Equal(t, "chat_completions", byEndpoint["/v1/chat/completions"].Operation)
	assert.Equal(t, 2, byEndpoint["/v1/chat/completions"].Requests)
	assert.Equal(t, "responses", byEndpoint["/v1/responses"].Operation)
	assert.Equal(t, 1, byEndpoint["/v1/responses"].Requests)

	chat := getAdminJSON[usage.UsageSummary](t, fixture.ServerURL+"/admin/usage/summary?days=30&operation=chat_completions")
	assert.Equal(t, 2, chat.TotalRequests)

	responses := getAdminJSON[usage.UsageSummary](t, fixture.ServerURL+"/admin/usage/summary?days=30&operation=responses")
	assert.Equal(t, 1, responses.TotalRequests, "the prefix match covers /v1/responses itself")

	exact := getAdminJSON[usage.UsageLogResult](t, fixture.ServerURL+"/admin/usage/log?days=30&endpoint=/v1/responses")
	assert.Equal(t, 1, exact.Total)

	both := getAdminJSON[usage.UsageSummary](t, fixture.ServerURL+"/admin/usage/summary?days=30&endpoint=/v1/responses,/v1/chat/completions")
	assert.Equal(t, 3, both.TotalRequests)

	fixture.FlushAndClose(t)
}

func getAdminJSON[T any](t *testing.T, url string) T {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer closeBody(resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var out T
	require.NoError(t, json.Unmarshal(body, &out))
	return out
}
