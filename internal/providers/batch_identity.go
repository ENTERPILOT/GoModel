package providers

import "github.com/enterpilot/gomodel/internal/core"

// EnsureProviderBatchID defaults the provider-facing batch ID to the response ID
// when an OpenAI-compatible upstream does not return a distinct one. No-op for a
// nil response or one that already carries a provider batch ID.
func EnsureProviderBatchID(resp *core.BatchResponse) {
	if resp != nil && resp.ProviderBatchID == "" {
		resp.ProviderBatchID = resp.ID
	}
}

// EnsureProviderBatchIDs applies EnsureProviderBatchID to every batch in a list
// response.
func EnsureProviderBatchIDs(resp *core.BatchListResponse) {
	if resp == nil {
		return
	}
	for i := range resp.Data {
		EnsureProviderBatchID(&resp.Data[i])
	}
}
