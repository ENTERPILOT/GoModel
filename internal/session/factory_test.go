package session

import (
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/stretchr/testify/require"
)

func TestNewDetectorFromConfigDisabled(t *testing.T) {
	d := NewDetectorFromConfig(config.SessionConfig{Enabled: false})
	require.Nil(t, d)
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
	got := detector.Detect(plain, "")
	require.Empty(t, got)

	embedded := chatSnapshot(map[string][]string{"X-Session-Id": {"user_x_session_12345678-1234-1234-1234-123456789012"}}, `{}`)
	got = detector.Detect(embedded, "")
	require.Equal(t, "12345678-1234-1234-1234-123456789012", got)

	custom := chatSnapshot(map[string][]string{"X-My-Conversation": {"conv-9"}}, `{}`)
	got = detector.Detect(custom, "")
	require.Equal(t, "conv-9", got)
}

func TestNewDetectorFromConfigWithoutBuiltins(t *testing.T) {
	detector := NewDetectorFromConfig(config.SessionConfig{
		Enabled: true,
		Headers: []config.SessionHeaderConfig{{Header: "X-My-Session"}},
	})
	builtin := chatSnapshot(map[string][]string{"X-Session-Id": {"ignored"}}, `{}`)
	got := detector.Detect(builtin, "")
	require.Empty(t, got)

	custom := chatSnapshot(map[string][]string{"X-My-Session": {"mine"}}, `{}`)
	got = detector.Detect(custom, "")
	require.Equal(t, "mine", got)
}
