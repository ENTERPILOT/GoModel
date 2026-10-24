package jev

import "github.com/enterpilot/gomodel/internal/providers"

// The keys are endpoints as a passthrough request spells them, without the
// optional /v1 alias the gateway strips before a provider sees them. The
// evaluation route is the hosted API's only inference endpoint; permute and
// separate are the diagnostic variants a Kev server adds. System One is not
// one of the standard GenAI operations, so none is claimed.
var passthroughSemanticEnricher = providers.NewSemanticEnricher("jev", map[string]providers.PassthroughEndpointSemantics{
	"/systemone": {
		Operation: "jev.systemone",
		AuditPath: "/v1/systemone",
	},
	"/systemone/permute": {
		Operation: "jev.systemone_permute",
		AuditPath: "/v1/systemone/permute",
	},
	"/systemone/separate": {
		Operation: "jev.systemone_separate",
		AuditPath: "/v1/systemone/separate",
	},
})
