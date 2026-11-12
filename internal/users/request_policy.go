package users

import (
	"context"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestparams"
)

// RequestPolicy is the request policy in force on one user path once its own
// row and every ancestor's are combined. Each *From field names the path the
// value comes from, empty when no path sets it.
type RequestPolicy struct {
	// DefaultModel comes from the nearest path that sets one: a child's
	// default replaces its group's.
	DefaultModel     string `json:"default_model,omitempty"`
	DefaultModelFrom string `json:"default_model_from,omitempty"`
	// MaxReasoningEffort and MaxOutputTokens are the lowest ceilings on the
	// way to the root: a child can tighten its group's ceiling, never loosen
	// it.
	MaxReasoningEffort     string `json:"max_reasoning_effort,omitempty"`
	MaxReasoningEffortFrom string `json:"max_reasoning_effort_from,omitempty"`
	MaxOutputTokens        int    `json:"max_output_tokens,omitempty"`
	MaxOutputTokensFrom    string `json:"max_output_tokens_from,omitempty"`
}

// IsZero reports whether no path sets any request policy.
func (p RequestPolicy) IsZero() bool {
	return p == RequestPolicy{}
}

// Ceilings returns the policy's ceilings in the form request overrides carry.
func (p RequestPolicy) Ceilings() requestparams.Ceilings {
	return requestparams.Ceilings{ReasoningEffort: p.MaxReasoningEffort, OutputTokens: p.MaxOutputTokens}
}

// RequestPolicy returns the request policy in force on userPath. A request
// without a user path has none, as it has no allowlist.
func (s *Service) RequestPolicy(userPath string) RequestPolicy {
	snap := s.snapshot()
	var policy RequestPolicy
	if !snap.requestPolicies {
		return policy
	}
	// Deepest first: the first default model seen is the nearest one, and a
	// ceiling is replaced only by a strictly lower one, so on a tie the
	// nearest path is the one reported.
	for _, ancestor := range core.UserPathAncestors(userPath) {
		user, ok := snap.byPath[ancestor]
		if !ok {
			continue
		}
		if policy.DefaultModel == "" && user.DefaultModel != "" {
			policy.DefaultModel, policy.DefaultModelFrom = user.DefaultModel, ancestor
		}
		own := requestparams.Ceilings{ReasoningEffort: user.MaxReasoningEffort, OutputTokens: user.MaxOutputTokens}
		tightest := policy.Ceilings().Tighten(own)
		if tightest.ReasoningEffort != policy.MaxReasoningEffort {
			policy.MaxReasoningEffort, policy.MaxReasoningEffortFrom = tightest.ReasoningEffort, ancestor
		}
		if tightest.OutputTokens != policy.MaxOutputTokens {
			policy.MaxOutputTokens, policy.MaxOutputTokensFrom = tightest.OutputTokens, ancestor
		}
	}
	return policy
}

// RequestCeilings returns the ceilings in force for the request's user path.
func (s *Service) RequestCeilings(ctx context.Context) requestparams.Ceilings {
	return s.RequestPolicy(core.UserPathFromContext(ctx)).Ceilings()
}

// DefaultModel returns the model that serves a request on the request's user
// path when it names none, or "" when no path sets one.
func (s *Service) DefaultModel(ctx context.Context) string {
	return s.RequestPolicy(core.UserPathFromContext(ctx)).DefaultModel
}

// hasRequestPolicy reports whether the row sets any request policy.
func (u User) hasRequestPolicy() bool {
	return u.DefaultModel != "" || u.MaxReasoningEffort != "" || u.MaxOutputTokens > 0
}

// normalizeRequestPolicy validates the request policy of one row in place.
func normalizeRequestPolicy(user *User) error {
	user.DefaultModel = strings.TrimSpace(user.DefaultModel)
	// A default is one model or virtual model, never a selector over many.
	if strings.ContainsAny(user.DefaultModel, "* \t\r\n") || strings.HasSuffix(user.DefaultModel, "/") {
		return newValidationError("invalid default_model "+user.DefaultModel+": name one model or virtual model", nil)
	}
	effort, err := requestparams.NormalizeReasoningEffort("max_reasoning_effort", user.MaxReasoningEffort)
	if err != nil {
		return err
	}
	user.MaxReasoningEffort = effort
	if user.MaxOutputTokens < 0 {
		return newValidationError("max_output_tokens must not be negative", nil)
	}
	return nil
}
