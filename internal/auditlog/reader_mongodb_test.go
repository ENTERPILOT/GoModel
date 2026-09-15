package auditlog

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestSanitizeLogDataRedactsHeaders(t *testing.T) {
	original := &LogData{
		RequestHeaders: map[string]string{
			"Authorization": "Bearer secret",
			"X-Test":        "ok",
		},
		ResponseHeaders: map[string]string{
			"Set-Cookie": "session=abc",
			"Server":     "gateway",
		},
	}

	sanitized := sanitizeLogData(original)
	require.NotNil(t, sanitized)
	require.Equal(t, "[REDACTED]", sanitized.RequestHeaders["Authorization"])
	require.Equal(t, "ok", sanitized.RequestHeaders["X-Test"])
	require.Equal(t, "[REDACTED]", sanitized.ResponseHeaders["Set-Cookie"])
	require.Equal(t, "gateway", sanitized.ResponseHeaders["Server"])
	// Ensure original is not mutated.
	require.Equal(t, "Bearer secret", original.RequestHeaders["Authorization"])
	require.Equal(t, "session=abc", original.ResponseHeaders["Set-Cookie"])
}

func TestSanitizeLogDataNilSafe(t *testing.T) {
	require.Nil(t, sanitizeLogData(nil))
}

func TestMongoLogRowToLogEntryPreservesCacheType(t *testing.T) {
	row := mongoLogRow{
		ID:             "log-1",
		RequestedModel: "gpt-4",
		Provider:       "openai",
		CacheType:      CacheTypeSemantic,
	}

	entry := row.toLogEntry()
	require.NotNil(t, entry)
	require.Equal(t, CacheTypeSemantic, entry.CacheType)
}

func TestMongoDBReader_GetLogsInvalidUserPathReturnsGatewayError(t *testing.T) {
	reader := &MongoDBReader{}

	_, err := reader.GetLogs(context.Background(), LogQueryParams{UserPath: "/team/../alpha"})
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
}

func TestMongoUserPathMatchFilter(t *testing.T) {
	t.Run("root includes regex plus legacy null or missing", func(t *testing.T) {
		got := mongoUserPathMatchFilter("/")
		want := bson.E{
			Key: "$or",
			Value: bson.A{
				bson.D{{Key: "user_path", Value: bson.D{{Key: "$regex", Value: "^/"}}}},
				bson.D{{Key: "user_path", Value: bson.D{{Key: "$exists", Value: false}}}},
				bson.D{{Key: "user_path", Value: nil}},
			},
		}
		require.Equal(t, want, got)
	})

	t.Run("non-root uses regex only", func(t *testing.T) {
		got := mongoUserPathMatchFilter("/team")
		want := bson.E{
			Key:   "user_path",
			Value: bson.D{{Key: "$regex", Value: "^/team(?:/|$)"}},
		}
		require.Equal(t, want, got)
	})
}

func TestMongoExactUserPathMatchFilter(t *testing.T) {
	t.Run("root includes only root and legacy rows", func(t *testing.T) {
		got := mongoExactUserPathMatchFilter("/")
		want := bson.E{
			Key: "$or",
			Value: bson.A{
				bson.D{{Key: "user_path", Value: "/"}},
				bson.D{{Key: "user_path", Value: ""}},
				bson.D{{Key: "user_path", Value: bson.D{{Key: "$exists", Value: false}}}},
				bson.D{{Key: "user_path", Value: nil}},
			},
		}
		require.Equal(t, want, got)
	})

	t.Run("non-root uses equality", func(t *testing.T) {
		got := mongoExactUserPathMatchFilter("/team")
		want := bson.E{Key: "user_path", Value: "/team"}
		require.Equal(t, want, got)
	})
}
