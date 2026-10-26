package ext

import "github.com/enterpilot/gomodel/pluginapi"

// Inference calls models through the gateway on behalf of an extension: chat
// completions and decision-model questions. Model resolution, access
// policies, failover, usage accounting, and audit apply as for a plugin's
// internal calls. Calls carry origin "plugin", skip guardrails, and are
// scoped to "<request user path>/extensions/<extension name>", so usage and
// the audit trail attribute them to the extension.
type Inference interface {
	pluginapi.Inference
	pluginapi.Decisions
}

// InferenceAware is optionally implemented by a request rewriter that calls
// models while it rewrites, such as a router asking a decision model how hard
// a request is. Core installs the handle once the gateway's internal
// executors are built, before serving, and installs a fresh one for every
// configuration generation; requests of the previous generation may still be
// running, so implementations must swap it atomically. Until a handle is
// installed, a rewriter must treat inference as unavailable.
type InferenceAware interface {
	SetInference(Inference)
}
