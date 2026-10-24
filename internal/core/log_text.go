package core

import "strings"

// LogText returns s without carriage returns or line feeds, for logging a
// name an admin API caller chose (a provider credential, MCP server, or
// guardrail name) so it cannot start a forged log line under a text handler.
func LogText(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", "")
}
