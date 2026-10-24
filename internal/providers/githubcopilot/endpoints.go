package githubcopilot

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"
)

const (
	chatEndpoint      = "/chat/completions"
	responsesEndpoint = "/responses"
	// routesRefetchInterval bounds how often a request for a model the
	// provider has not seen re-reads /models, so traffic to an unlisted or
	// mistyped model cannot turn into one listing call per request.
	routesRefetchInterval = time.Minute
	// routesFetchTimeout bounds a refetch made on behalf of a request. The
	// refetch ignores that request's cancellation, so one client hanging up
	// cannot spend the refetch interval on a failed listing.
	routesFetchTimeout = 10 * time.Second
)

// modelRoutes holds the endpoints each model is served on, as /models
// reports them in supported_endpoints. A model without the field (the older
// GPT-4 family) is served on /chat/completions.
type modelRoutes struct {
	mu        sync.RWMutex
	endpoints map[string][]string
	fetchedAt time.Time

	// fetchMu lets one request at a time refetch the listing, so concurrent
	// requests for an unknown model wait for it instead of misrouting.
	fetchMu sync.Mutex
}

// set replaces the known endpoints with a fresh listing.
func (r *modelRoutes) set(endpoints map[string][]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.endpoints = endpoints
	r.fetchedAt = time.Now()
}

func (r *modelRoutes) get(model string) ([]string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	endpoints, ok := r.endpoints[model]
	return endpoints, ok
}

// claimFetch reports whether the listing may be refetched now, and if so
// records the attempt, so a failing /models is retried at most once per
// routesRefetchInterval.
func (r *modelRoutes) claimFetch() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.fetchedAt) < routesRefetchInterval {
		return false
	}
	r.fetchedAt = time.Now()
	return true
}

// lookup returns the model's endpoints, reading /models through fetch when
// the model is unknown: the registry may serve a cached or configured model
// list without ever calling ListModels on this instance. A model still unknown
// after that, because the listing omits it or failed, gets no endpoints and is
// sent to /chat/completions. Most Copilot models are served there, so a
// /models outage keeps them working instead of failing every request.
func (r *modelRoutes) lookup(ctx context.Context, model string, fetch func(context.Context) error) []string {
	if endpoints, ok := r.get(model); ok {
		return endpoints
	}
	r.fetchMu.Lock()
	defer r.fetchMu.Unlock()
	if endpoints, ok := r.get(model); ok {
		return endpoints
	}
	if !r.claimFetch() {
		return nil
	}
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), routesFetchTimeout)
	defer cancel()
	if err := fetch(fetchCtx); err != nil {
		slog.Warn("github-copilot model listing failed; routing unknown models to chat completions", "model", model, "error", err)
	}
	endpoints, _ := r.get(model)
	return endpoints
}

// responsesOnly reports whether Copilot serves the model on /responses but
// not on /chat/completions.
func (p *Provider) responsesOnly(ctx context.Context, model string) bool {
	endpoints := p.routes.lookup(ctx, model, p.fetchModels)
	return slices.Contains(endpoints, responsesEndpoint) && !slices.Contains(endpoints, chatEndpoint)
}

// servesResponses reports whether Copilot serves the model on /responses.
func (p *Provider) servesResponses(ctx context.Context, model string) bool {
	return slices.Contains(p.routes.lookup(ctx, model, p.fetchModels), responsesEndpoint)
}
