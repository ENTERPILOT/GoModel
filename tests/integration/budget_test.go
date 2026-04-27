//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"gomodel/config"
	"gomodel/internal/budget"
	"gomodel/internal/core"
	"gomodel/tests/integration/dbassert"
)

const (
	integrationBudgetPath   = "/team/budget"
	integrationBudgetAmount = 0.01
)

func TestBudget_EnforcesAndPersistsAcrossDatabases(t *testing.T) {
	tests := []struct {
		name         string
		dbType       string
		auditEnabled bool
	}{
		{name: "postgresql audit enabled", dbType: "postgresql", auditEnabled: true},
		{name: "postgresql audit disabled", dbType: "postgresql", auditEnabled: false},
		{name: "mongodb audit enabled", dbType: "mongodb", auditEnabled: true},
		{name: "mongodb audit disabled", dbType: "mongodb", auditEnabled: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := SetupTestServer(t, TestServerConfig{
				DBType:                tt.dbType,
				AuditLogEnabled:       tt.auditEnabled,
				UsageEnabled:          true,
				BudgetsEnabled:        true,
				LogBodies:             tt.auditEnabled,
				AdminEndpointsEnabled: true,
				OnlyModelInteractions: false,
				BudgetUserPaths: []config.BudgetUserPathConfig{
					{
						Path: integrationBudgetPath,
						Limits: []config.BudgetLimitConfig{
							{Period: "daily", Amount: integrationBudgetAmount},
						},
					},
				},
			})
			defer fixture.Shutdown(t)

			budgets := dbassert.QueryBudgetsForFixture(t, tt.dbType, fixture.PgPool, fixture.MongoDb)
			dbassert.AssertOneSeededBudget(t, budgets, integrationBudgetPath, budget.PeriodDailySeconds, integrationBudgetAmount)

			firstRequestID := uuid.NewString()
			firstResp := sendChatRequestWithHeaders(t, fixture.ServerURL, newChatRequest("gpt-4", "first"), map[string]string{
				"X-Request-ID":          firstRequestID,
				"X-GoModel-User-Path":   integrationBudgetPath + "/app",
				"X-GoModel-Test-Budget": tt.name,
			})
			require.Equal(t, http.StatusOK, firstResp.StatusCode)
			closeBody(firstResp)

			firstUsage := waitForBudgetUsage(t, fixture, firstRequestID)
			require.Equal(t, integrationBudgetPath+"/app", firstUsage.UserPath)
			require.NotNil(t, firstUsage.TotalCost, "first request should have calculated cost")
			require.Greater(t, *firstUsage.TotalCost, integrationBudgetAmount)

			secondRequestID := uuid.NewString()
			secondResp := sendChatRequestWithHeaders(t, fixture.ServerURL, newChatRequest("gpt-4", "second"), map[string]string{
				"X-Request-ID":        secondRequestID,
				"X-GoModel-User-Path": integrationBudgetPath + "/app",
			})
			defer closeBody(secondResp)
			require.Equal(t, http.StatusTooManyRequests, secondResp.StatusCode)
			require.NotEmpty(t, secondResp.Header.Get("Retry-After"))

			var errorBody core.OpenAIErrorEnvelope
			require.NoError(t, json.NewDecoder(secondResp.Body).Decode(&errorBody))
			require.Equal(t, core.ErrorTypeRateLimit, errorBody.Error.Type)
			require.NotNil(t, errorBody.Error.Code)
			require.Equal(t, "budget_exceeded", *errorBody.Error.Code)

			require.Empty(t, usageEntriesByRequestID(t, fixture, secondRequestID), "blocked request must not write usage")
			require.Len(t, fixture.MockLLM.Requests(), 1, "blocked request must not reach upstream provider")

			if tt.auditEnabled {
				auditEntry := waitForAuditEntry(t, fixture, secondRequestID)
				require.Equal(t, http.StatusTooManyRequests, auditEntry.StatusCode)
				require.Equal(t, integrationBudgetPath+"/app", auditEntry.UserPath)
				require.Equal(t, string(core.ErrorTypeRateLimit), auditEntry.ErrorType)
				if auditEntry.Data != nil {
					require.Equal(t, "budget_exceeded", auditEntry.Data.ErrorCode)
				}
			} else {
				require.False(t, dbassert.StorageObjectExists(t, tt.dbType, fixture.PgPool, fixture.MongoDb, "audit_logs"))
			}
		})
	}
}

func waitForBudgetUsage(t *testing.T, fixture *TestServerFixture, requestID string) dbassert.UsageEntry {
	t.Helper()

	var entries []dbassert.UsageEntry
	require.Eventually(t, func() bool {
		entries = usageEntriesByRequestID(t, fixture, requestID)
		return len(entries) == 1 && entries[0].TotalCost != nil
	}, 5*time.Second, 100*time.Millisecond)

	return entries[0]
}

func usageEntriesByRequestID(t *testing.T, fixture *TestServerFixture, requestID string) []dbassert.UsageEntry {
	t.Helper()

	switch fixture.DBType {
	case "postgresql":
		return dbassert.QueryUsageByRequestID(t, fixture.PgPool, requestID)
	case "mongodb":
		return dbassert.QueryUsageByRequestIDMongo(t, fixture.MongoDb, requestID)
	default:
		t.Fatalf("unsupported DB type %q", fixture.DBType)
		return nil
	}
}

func waitForAuditEntry(t *testing.T, fixture *TestServerFixture, requestID string) dbassert.AuditLogEntry {
	t.Helper()

	var entries []dbassert.AuditLogEntry
	require.Eventually(t, func() bool {
		switch fixture.DBType {
		case "postgresql":
			entries = dbassert.QueryAuditLogsByRequestID(t, fixture.PgPool, requestID)
		case "mongodb":
			entries = dbassert.QueryAuditLogsByRequestIDMongo(t, fixture.MongoDb, requestID)
		default:
			t.Fatalf("unsupported DB type %q", fixture.DBType)
		}
		return len(entries) == 1
	}, 5*time.Second, 100*time.Millisecond)

	return entries[0]
}
