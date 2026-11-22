package opencodego

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/internal/llmclient"
)

// The API names OpenCode Zen publishes in each model's "api" member of
// GET /models. A model lists the dialect it is served on natively; the others
// are reached through OpenCode's own translation layer.
const (
	chatAPI      = "chat-completions"
	responsesAPI = "responses"
)

const (
	// routesRefetchInterval bounds how often a request for a model the
	// provider has not seen re-reads /models, so traffic to an unlisted or
	// mistyped model cannot turn into one listing call per request.
	routesRefetchInterval = time.Minute
	// routesFetchTimeout bounds a refetch made on behalf of a request. The
	// refetch ignores that request's cancellation, so one client hanging up
	// cannot spend the refetch interval on a failed listing.
	routesFetchTimeout = 10 * time.Second
)

// modelRoutes holds the API each model is served on, as GET /models reports it
// in the "api" member. A model the listing omits, or a listing that failed,
// leaves the model unknown: it is then served on /chat/completions, where
// OpenCode Zen translates the request itself.
type modelRoutes struct {
	mu        sync.RWMutex
	apis      map[string][]string
	fetchedAt time.Time

	// fetchMu lets one request at a time refetch the listing, so concurrent
	// requests for an unknown model wait for it instead of misrouting.
	fetchMu sync.Mutex
}

// set replaces the known APIs with a fresh listing.
func (r *modelRoutes) set(apis map[string][]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.apis = apis
	r.fetchedAt = time.Now()
}

func (r *modelRoutes) get(model string) ([]string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	apis, ok := r.apis[model]
	return apis, ok
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

// lookup returns the APIs the model is served on, reading /models through
// fetch when the model is unknown: the registry may serve a cached or
// configured model list without ever calling ListModels on this instance. A
// model still unknown after that, because the listing omits it or failed, gets
// no APIs and is sent to /chat/completions.
func (r *modelRoutes) lookup(ctx context.Context, model string, fetch func(context.Context) error) []string {
	if apis, ok := r.get(model); ok {
		return apis
	}
	r.fetchMu.Lock()
	defer r.fetchMu.Unlock()
	if apis, ok := r.get(model); ok {
		return apis
	}
	if !r.claimFetch() {
		return nil
	}
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), routesFetchTimeout)
	defer cancel()
	if err := fetch(fetchCtx); err != nil {
		slog.Warn("opencode_go model listing failed; routing unknown models to chat completions",
			"model", model, "error", err)
	}
	apis, _ := r.get(model)
	return apis
}

// fetchModelAPIs refreshes the per-model APIs from GET /models, whose entries
// carry the native dialect in "api" ({"chat-completions":{…}} and so on).
func (p *Provider) fetchModelAPIs(ctx context.Context) error {
	var listing struct {
		Data []struct {
			ID string `json:"id"`
			// API maps each dialect name to its connection details, which
			// routing does not need: only the set of names matters.
			API map[string]struct{} `json:"api"`
		} `json:"data"`
	}
	if err := p.Do(llmclient.WithoutCircuitBreaker(ctx), llmclient.Request{Method: http.MethodGet, Endpoint: "/models"}, &listing); err != nil {
		return err
	}
	apis := make(map[string][]string, len(listing.Data))
	for _, entry := range listing.Data {
		if entry.ID == "" {
			continue
		}
		names := make([]string, 0, len(entry.API))
		for name := range entry.API {
			names = append(names, name)
		}
		apis[entry.ID] = names
	}
	p.routes.set(apis)
	return nil
}

// responsesOnly reports whether OpenCode Zen serves the model on /responses but
// not on /chat/completions: a chat request for it must be translated instead of
// sent to an endpoint that rejects it with "Model does not support this
// protocol".
func (p *Provider) responsesOnly(ctx context.Context, model string) bool {
	apis := p.routes.lookup(ctx, bareModel(model), p.fetchModelAPIs)
	return slices.Contains(apis, responsesAPI) && !slices.Contains(apis, chatAPI)
}

// servesResponses reports whether OpenCode Zen serves the model on /responses,
// so a Responses request can be forwarded instead of translated down to chat.
func (p *Provider) servesResponses(ctx context.Context, model string) bool {
	return slices.Contains(p.routes.lookup(ctx, bareModel(model), p.fetchModelAPIs), responsesAPI)
}

// bareModel drops a provider prefix the router keeps on the model name, so
// "opencode_go/grok-4.7" matches the listing's "grok-4.7".
func bareModel(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		return model[i+1:]
	}
	return model
}
