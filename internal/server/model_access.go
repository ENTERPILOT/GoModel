package server

import "github.com/enterpilot/gomodel/internal/gateway"

// RequestModelAuthorizer validates request-scoped access to concrete models.
type RequestModelAuthorizer = gateway.ModelAuthorizer
