package auditlog

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"gomodel/internal/core"

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
	if sanitized == nil {
		t.Fatalf("sanitizeLogData returned nil")
		return
	}

	if got := sanitized.RequestHeaders["Authorization"]; got != "[REDACTED]" {
		t.Fatalf("request Authorization not redacted: %q", got)
	}
	if got := sanitized.RequestHeaders["X-Test"]; got != "ok" {
		t.Fatalf("request non-sensitive header changed: %q", got)
	}
	if got := sanitized.ResponseHeaders["Set-Cookie"]; got != "[REDACTED]" {
		t.Fatalf("response Set-Cookie not redacted: %q", got)
	}
	if got := sanitized.ResponseHeaders["Server"]; got != "gateway" {
		t.Fatalf("response non-sensitive header changed: %q", got)
	}

	// Ensure original is not mutated.
	if got := original.RequestHeaders["Authorization"]; got != "Bearer secret" {
		t.Fatalf("original request headers mutated: %q", got)
	}
	if got := original.ResponseHeaders["Set-Cookie"]; got != "session=abc" {
		t.Fatalf("original response headers mutated: %q", got)
	}
}

func TestSanitizeLogDataNilSafe(t *testing.T) {
	if sanitizeLogData(nil) != nil {
		t.Fatalf("expected nil input to return nil")
	}
}

func TestMongoLogRowToLogEntryPreservesCacheType(t *testing.T) {
	row := mongoLogRow{
		ID:        "log-1",
		Model:     "gpt-4",
		Provider:  "openai",
		CacheType: CacheTypeSemantic,
	}

	entry := row.toLogEntry()
	if entry == nil {
		t.Fatal("expected entry, got nil")
		return
	}
	if entry.CacheType != CacheTypeSemantic {
		t.Fatalf("CacheType = %q, want %q", entry.CacheType, CacheTypeSemantic)
	}
}

func TestMongoDBReader_GetLogsInvalidUserPathReturnsGatewayError(t *testing.T) {
	reader := &MongoDBReader{}

	_, err := reader.GetLogs(context.Background(), LogQueryParams{UserPath: "/team/../alpha"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("expected GatewayError, got %T", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("gatewayErr.Type = %q, want %q", gatewayErr.Type, core.ErrorTypeInvalidRequest)
	}
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
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("mongoUserPathMatchFilter(%q) = %#v, want %#v", "/", got, want)
		}
	})

	t.Run("non-root uses regex only", func(t *testing.T) {
		got := mongoUserPathMatchFilter("/team")
		want := bson.E{
			Key:   "user_path",
			Value: bson.D{{Key: "$regex", Value: "^/team(?:/|$)"}},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("mongoUserPathMatchFilter(%q) = %#v, want %#v", "/team", got, want)
		}
	})
}
