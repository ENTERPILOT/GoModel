package session

import (
	"testing"

	"github.com/enterpilot/gomodel/config"
)

func TestNewDetectorFromConfigDisabled(t *testing.T) {
	if d := NewDetectorFromConfig(config.SessionConfig{Enabled: false}); d != nil {
		t.Fatal("disabled config must yield a nil detector")
	}
}

func TestNewDetectorFromConfigOverridesBuiltinHeader(t *testing.T) {
	detector := NewDetectorFromConfig(config.SessionConfig{
		Enabled:      true,
		BuiltinRules: true,
		Headers: []config.SessionHeaderConfig{
			{Header: "X-Session-Id", Transform: TransformSessionUUID},
			{Header: "X-My-Conversation"},
		},
	})

	// The overridden builtin now requires the transform to match.
	plain := chatSnapshot(map[string][]string{"X-Session-Id": {"plain-value"}}, `{}`)
	if got := detector.Detect(plain, ""); got != "" {
		t.Fatalf("overridden rule must apply the transform, got %q", got)
	}
	embedded := chatSnapshot(map[string][]string{"X-Session-Id": {"user_x_session_12345678-1234-1234-1234-123456789012"}}, `{}`)
	if got := detector.Detect(embedded, ""); got != "12345678-1234-1234-1234-123456789012" {
		t.Fatalf("Detect() = %q, want extracted uuid", got)
	}

	custom := chatSnapshot(map[string][]string{"X-My-Conversation": {"conv-9"}}, `{}`)
	if got := detector.Detect(custom, ""); got != "conv-9" {
		t.Fatalf("Detect() = %q, want custom header value", got)
	}
}

func TestNewDetectorFromConfigWithoutBuiltins(t *testing.T) {
	detector := NewDetectorFromConfig(config.SessionConfig{
		Enabled: true,
		Headers: []config.SessionHeaderConfig{{Header: "X-My-Session"}},
	})
	builtin := chatSnapshot(map[string][]string{"X-Session-Id": {"ignored"}}, `{}`)
	if got := detector.Detect(builtin, ""); got != "" {
		t.Fatalf("builtin rules disabled, got %q", got)
	}
	custom := chatSnapshot(map[string][]string{"X-My-Session": {"mine"}}, `{}`)
	if got := detector.Detect(custom, ""); got != "mine" {
		t.Fatalf("Detect() = %q, want configured header value", got)
	}
}
