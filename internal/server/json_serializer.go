package server

import (
	"github.com/goccy/go-json"
	"github.com/labstack/echo/v5"
)

// goJSONSerializer serializes responses with goccy/go-json instead of echo's
// reflection-based encoding/json default. Response types already marshal with
// goccy elsewhere (internal/core), so this removes the second, slower encoder
// from the hot path. Deserialization keeps echo's default: request bodies are
// decoded through the core types, not c.Bind, so there is nothing to win there
// and the default's error mapping stays intact for the few admin binds.
type goJSONSerializer struct {
	fallback echo.DefaultJSONSerializer
}

func (s goJSONSerializer) Serialize(c *echo.Context, target any, indent string) error {
	enc := json.NewEncoder(c.Response())
	if indent != "" {
		enc.SetIndent("", indent)
	}
	return enc.Encode(target)
}

func (s goJSONSerializer) Deserialize(c *echo.Context, target any) error {
	return s.fallback.Deserialize(c, target)
}
