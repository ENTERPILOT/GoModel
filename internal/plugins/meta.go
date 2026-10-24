package plugins

import (
	"context"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

// MetaFromContext snapshots the request facts a hook may read from the
// request context and the resolved workflow. Attempts are not read here:
// callers of response phases add them with WithAttempts.
func MetaFromContext(ctx context.Context, workflow *core.Workflow) pluginapi.Meta {
	if ctx == nil {
		ctx = context.Background()
	}
	meta := pluginapi.Meta{
		RequestID: strings.TrimSpace(core.GetRequestID(ctx)),
		Dialect:   dialect(ctx),
		UserPath:  core.UserPathFromContext(ctx),
		AuthKeyID: core.GetAuthKeyID(ctx),
		SessionID: core.SessionIDFromContext(ctx),
		Origin:    string(core.GetRequestOrigin(ctx)),
		Labels:    labels(core.RequestLabelsFromContext(ctx)),
	}
	if snapshot := core.GetRequestSnapshot(ctx); snapshot != nil {
		meta.Endpoint = snapshot.Path
	}
	if workflow == nil {
		return meta
	}
	if meta.RequestID == "" {
		meta.RequestID = strings.TrimSpace(workflow.RequestID)
	}
	meta.Operation = string(workflow.Endpoint.Operation)
	if meta.Endpoint == "" {
		meta.Endpoint = endpointPath(workflow.Endpoint.Operation)
	}
	meta.Provider = workflow.ProviderType
	if resolution := workflow.Resolution; resolution != nil {
		meta.RequestedModel = resolution.RequestedQualifiedModel()
		meta.Model = resolution.ResolvedSelector.Model
		meta.ProviderName = resolution.ProviderName
		if meta.Provider == "" {
			meta.Provider = resolution.ProviderType
		}
		if resolution.AliasApplied {
			meta.VirtualModelSource = resolution.RequestedQualifiedModel()
		}
	}
	if policy := workflow.Policy; policy != nil {
		meta.WorkflowVersionID = strings.TrimSpace(policy.VersionID)
		meta.Features = map[string]bool{
			"cache":      policy.Features.Cache,
			"audit":      policy.Features.Audit,
			"usage":      policy.Features.Usage,
			"budget":     policy.Features.Budget,
			"guardrails": policy.Features.Guardrails,
			"failover":   policy.Features.Failover,
		}
	}
	return meta
}

// Attempt mirrors one provider attempt without importing the gateway package.
type Attempt struct {
	Seq          int
	Kind         string
	ProviderType string
	ProviderName string
	Model        string
	StatusCode   int
	Success      bool
	ErrorCode    string
	Duration     time.Duration
}

// WithAttempts returns meta with the provider attempts filled in.
func WithAttempts(meta pluginapi.Meta, attempts []Attempt) pluginapi.Meta {
	if len(attempts) == 0 {
		return meta
	}
	meta.Attempts = make([]pluginapi.Attempt, 0, len(attempts))
	for _, a := range attempts {
		meta.Attempts = append(meta.Attempts, pluginapi.Attempt{
			Seq:          a.Seq,
			Kind:         a.Kind,
			Provider:     a.ProviderType,
			ProviderName: a.ProviderName,
			Model:        a.Model,
			StatusCode:   a.StatusCode,
			Success:      a.Success,
			ErrorCode:    a.ErrorCode,
			Duration:     a.Duration,
		})
	}
	return meta
}

func endpointPath(op core.Operation) string {
	switch op {
	case core.OperationChatCompletions:
		return "/v1/chat/completions"
	case core.OperationResponses:
		return "/v1/responses"
	case core.OperationBatches:
		return "/v1/batches"
	default:
		return ""
	}
}

func dialect(ctx context.Context) string {
	if d := core.RequestDialectFromContext(ctx); d != "" {
		return string(d)
	}
	return "openai"
}

func labels(values []string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for _, value := range values {
		key, val, _ := strings.Cut(value, "=")
		out[key] = val
	}
	return out
}
