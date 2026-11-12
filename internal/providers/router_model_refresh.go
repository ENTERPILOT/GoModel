package providers

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/enterpilot/gomodel/internal/core"
)

const (
	// onDemandRefreshCooldown is how long a request-time model list refresh
	// for one provider selector is reused before another request may fetch
	// again. It bounds the upstream GET /models calls that requests for
	// unknown models can cause, while a model added upstream still becomes
	// routable within this window.
	onDemandRefreshCooldown = time.Minute
	// onDemandRefreshTimeout bounds a shared refresh, which runs detached
	// from the cancellation of the request that started it.
	onDemandRefreshTimeout = 30 * time.Second
)

// onDemandRefresher throttles request-time provider model refreshes:
// concurrent requests for the same provider selector share one fetch, and a
// selector refreshed within onDemandRefreshCooldown reuses that result
// instead of calling the provider again. The zero value is ready to use.
type onDemandRefresher struct {
	flight singleflight.Group

	mu   sync.Mutex
	last map[string]onDemandRefreshResult
}

type onDemandRefreshResult struct {
	at    time.Time
	count int
	err   error
}

// refresh returns the recent result for providerSelector when one exists,
// and otherwise runs fetch once for all concurrent callers.
func (o *onDemandRefresher) refresh(
	ctx context.Context,
	providerSelector string,
	fetch func(context.Context, string) (int, error),
) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if result, ok := o.recent(providerSelector); ok {
		return result.count, result.err
	}

	// The fetch is shared, so one caller giving up must not fail it for the
	// others; the timeout still bounds it.
	shared := context.WithoutCancel(ctx)
	ch := o.flight.DoChan(providerSelector, func() (any, error) {
		if result, ok := o.recent(providerSelector); ok {
			return result.count, result.err
		}
		fetchCtx, cancel := context.WithTimeout(shared, onDemandRefreshTimeout)
		defer cancel()
		count, err := fetch(fetchCtx, providerSelector)
		o.record(providerSelector, onDemandRefreshResult{at: time.Now(), count: count, err: err})
		return count, err
	})
	select {
	case res := <-ch:
		count, _ := res.Val.(int)
		return count, res.Err
	case <-ctx.Done():
		return 0, onDemandRefreshWaitError(ctx.Err())
	}
}

func (o *onDemandRefresher) recent(providerSelector string) (onDemandRefreshResult, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	result, ok := o.last[providerSelector]
	if !ok || time.Since(result.at) >= onDemandRefreshCooldown {
		return onDemandRefreshResult{}, false
	}
	return result, true
}

func (o *onDemandRefresher) record(providerSelector string, result onDemandRefreshResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.last == nil {
		o.last = make(map[string]onDemandRefreshResult)
	}
	// Drop expired results so selectors of removed providers do not linger.
	for selector, prev := range o.last {
		if result.at.Sub(prev.at) >= onDemandRefreshCooldown {
			delete(o.last, selector)
		}
	}
	o.last[providerSelector] = result
}

func onDemandRefreshWaitError(err error) *core.GatewayError {
	if errors.Is(err, context.DeadlineExceeded) {
		return core.NewProviderError("model_registry", http.StatusGatewayTimeout, "model registry refresh timed out", err)
	}
	return core.NewProviderError("model_registry", http.StatusRequestTimeout, "model registry refresh canceled", err)
}
