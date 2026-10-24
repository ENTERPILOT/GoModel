package server

import (
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/config"
)

// ClientIPExtractor turns a resolved client-address policy into the strategy
// echo uses for c.RealIP(), so every consumer — audit entries, rate limit
// keys, request logs — reports the same address. A policy that trusts no
// proxy returns nil, leaving the direct extraction the server defaults to.
func ClientIPExtractor(policy config.ClientIPPolicy) echo.IPExtractor {
	if !policy.Enabled() {
		return nil
	}
	return policy.Resolve
}
