package core

import (
	"context"
	"sync/atomic"
)

// genAIOperationKey stores the request's GenAIOperation.
const genAIOperationKey contextKey = "genai-operation"

// GenAIOperation records the GenAI operation (chat, generate_content, …) of
// the provider calls made for one request, as the provider adapter named it
// for the duration metric. Telemetry attaches it to the request and sets it
// on every provider call; the usage entry reads it back, so the token metrics
// name the same operation as the duration metric. Several calls for one
// request (a failover) leave the last one. A nil record is a no-op.
type GenAIOperation struct {
	name atomic.Pointer[string]
}

// WithGenAIOperation attaches an empty operation record to ctx.
func WithGenAIOperation(ctx context.Context) context.Context {
	return context.WithValue(ctx, genAIOperationKey, &GenAIOperation{})
}

// GenAIOperationFromContext returns the request's operation record, or nil.
func GenAIOperationFromContext(ctx context.Context) *GenAIOperation {
	if ctx == nil {
		return nil
	}
	record, _ := ctx.Value(genAIOperationKey).(*GenAIOperation)
	return record
}

// Set records the operation of a provider call.
func (o *GenAIOperation) Set(name string) {
	if o != nil && name != "" {
		o.name.Store(&name)
	}
}

// Name returns the recorded operation, or "".
func (o *GenAIOperation) Name() string {
	if o == nil {
		return ""
	}
	if name := o.name.Load(); name != nil {
		return *name
	}
	return ""
}
