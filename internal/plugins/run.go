package plugins

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
)

// Record is one instance's contribution to a chain run, kept for audit.
type Record struct {
	Instance string
	Decision pluginapi.Decision
	Duration time.Duration
	// Err is set when the instance failed; a fail-open failure still leaves
	// the chain running.
	Err error
}

// Outcome is the merged result of a chain run.
type Outcome struct {
	// Decision is the most severe decision of the run (allow when nothing
	// objected). Blocking decisions end the chain after their step.
	Decision pluginapi.Decision
	// Instance names the instance that produced Decision, when not allow.
	Instance string
	Records  []Record
}

type hookCall func(ctx context.Context, inst *Instance, x *pluginapi.Exchange) (pluginapi.Decision, error)

// RunPrompt runs the chain's OnPrompt hooks over x.
func (c *Chain) RunPrompt(ctx context.Context, x *pluginapi.Exchange) (Outcome, error) {
	return c.run(ctx, x, func(ctx context.Context, inst *Instance, x *pluginapi.Exchange) (pluginapi.Decision, error) {
		hook, ok := inst.Plugin.(pluginapi.PromptHook)
		if !ok {
			return pluginapi.Allow(), nil
		}
		return hook.OnPrompt(ctx, x)
	})
}

// RunResponse runs the chain's OnResponse hooks over x.
func (c *Chain) RunResponse(ctx context.Context, x *pluginapi.Exchange) (Outcome, error) {
	return c.run(ctx, x, func(ctx context.Context, inst *Instance, x *pluginapi.Exchange) (pluginapi.Decision, error) {
		hook, ok := inst.Plugin.(pluginapi.ResponseHook)
		if !ok {
			return pluginapi.Allow(), nil
		}
		return hook.OnResponse(ctx, x)
	})
}

// RunStreamEnd runs the chain's OnStreamEnd hooks over x.
func (c *Chain) RunStreamEnd(ctx context.Context, x *pluginapi.Exchange) (Outcome, error) {
	return c.run(ctx, x, func(ctx context.Context, inst *Instance, x *pluginapi.Exchange) (pluginapi.Decision, error) {
		hook, ok := inst.Plugin.(pluginapi.StreamHook)
		if !ok {
			return pluginapi.Allow(), nil
		}
		return hook.OnStreamEnd(ctx, x)
	})
}

// run executes the steps in order. Within a step the non-mutating instances
// run concurrently on a shallow copy of the exchange (their Values and
// response headers are merged back afterwards), then the mutating one runs
// on the exchange itself. The first blocking decision ends the chain after
// its step completes.
func (c *Chain) run(ctx context.Context, x *pluginapi.Exchange, call hookCall) (Outcome, error) {
	outcome := Outcome{Decision: pluginapi.Allow()}
	if c.Empty() || x == nil {
		return outcome, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ensureExchange(x)
	for _, step := range c.Steps {
		var readers []*Instance
		var mutator *Instance
		for _, inst := range step.Instances {
			if inst.Mutates() {
				mutator = inst
				continue
			}
			readers = append(readers, inst)
		}
		records, err := c.runReaders(ctx, readers, x, call)
		outcome.absorb(records)
		if err != nil {
			return outcome, err
		}
		if mutator != nil {
			record, err := c.invoke(ctx, mutator, x, call)
			outcome.absorb([]Record{record})
			if err != nil {
				return outcome, err
			}
		}
		if outcome.Decision.Blocks() {
			return outcome, nil
		}
	}
	return outcome, nil
}

func (c *Chain) runReaders(ctx context.Context, readers []*Instance, x *pluginapi.Exchange, call hookCall) ([]Record, error) {
	switch len(readers) {
	case 0:
		return nil, nil
	case 1:
		record, err := c.invoke(ctx, readers[0], x, call)
		return []Record{record}, err
	}
	records := make([]Record, len(readers))
	errs := make([]error, len(readers))
	copies := make([]*pluginapi.Exchange, len(readers))
	original := x.Headers.Request.Clone()
	var wg sync.WaitGroup
	for i, inst := range readers {
		copies[i] = shallowCopy(x)
		wg.Add(1)
		go func(i int, inst *Instance, xi *pluginapi.Exchange) {
			defer wg.Done()
			records[i], errs[i] = c.invoke(ctx, inst, xi, call)
		}(i, inst, copies[i])
	}
	wg.Wait()
	for _, xi := range copies {
		mergeBack(x, xi, original)
	}
	for _, err := range errs {
		if err != nil {
			return records, err
		}
	}
	return records, nil
}

// invoke calls one instance with timeout, panic recovery and fail-mode
// handling. A fail-closed failure returns a *PluginError; a fail-open one is
// logged and recorded with an allow decision.
func (c *Chain) invoke(ctx context.Context, inst *Instance, x *pluginapi.Exchange, call hookCall) (Record, error) {
	start := time.Now()
	decision, err := callHook(ctx, inst, x, call)
	record := Record{Instance: inst.Name, Decision: NormalizeDecision(decision), Duration: time.Since(start), Err: err}
	if err == nil {
		return record, nil
	}
	record.Decision = pluginapi.Allow()
	if inst.EffectiveFailMode(c.Phase) == FailOpen {
		slog.Warn("plugin instance failed; continuing (fail_open)",
			"plugin", inst.Type, "instance", inst.Name, "phase", string(c.Phase), "error", err)
		return record, nil
	}
	return record, &PluginError{Instance: inst.Name, Phase: c.Phase, Err: err}
}

func callHook(ctx context.Context, inst *Instance, x *pluginapi.Exchange, call hookCall) (pluginapi.Decision, error) {
	return Call(ctx, inst, func(ctx context.Context) (pluginapi.Decision, error) {
		return call(ctx, inst, x)
	})
}

// Call runs fn under the instance's timeout with panic recovery. A call
// that outlives its timeout reports an error even when fn returned nil.
func Call[T any](ctx context.Context, inst *Instance, fn func(context.Context) (T, error)) (result T, err error) {
	if inst != nil && inst.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, inst.Timeout)
		defer cancel()
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("plugin panicked: %v", r)
		}
	}()
	result, err = fn(ctx)
	if err == nil && inst != nil && inst.Timeout > 0 && ctx.Err() != nil {
		err = fmt.Errorf("plugin exceeded its %s timeout", inst.Timeout)
	}
	return result, err
}

func (o *Outcome) absorb(records []Record) {
	for _, record := range records {
		o.Records = append(o.Records, record)
		if record.Err != nil {
			continue
		}
		if Severity(record.Decision.Action) > Severity(o.Decision.Action) {
			o.Decision = record.Decision
			o.Instance = record.Instance
		}
	}
}

// DefaultBlockStatus returns the block status a phase uses when the decision
// sets none.
func DefaultBlockStatus(phase pluginapi.Kind) int {
	switch phase {
	case pluginapi.KindResponse, pluginapi.KindStream:
		return http.StatusBadGateway
	default:
		return http.StatusBadRequest
	}
}

func ensureExchange(x *pluginapi.Exchange) {
	if x.Values == nil {
		x.Values = pluginapi.Values{}
	}
	if x.Headers == nil {
		x.Headers = &pluginapi.Headers{}
	}
	if x.Headers.Response == nil {
		x.Headers.Response = http.Header{}
	}
}

// shallowCopy gives a concurrent reader its own Values and Headers so two
// readers of one step never write the same map.
func shallowCopy(x *pluginapi.Exchange) *pluginapi.Exchange {
	cp := *x
	cp.Values = make(pluginapi.Values, len(x.Values))
	maps.Copy(cp.Values, x.Values)
	headers := *x.Headers
	headers.Request = x.Headers.Request.Clone()
	headers.Response = x.Headers.Response.Clone()
	if headers.Upstream != nil {
		headers.Upstream = headers.Upstream.Clone()
	}
	cp.Headers = &headers
	return &cp
}

// mergeBack folds a reader's copy into x. Request header edits are applied as
// differences from original (the headers before the step ran), so a reader
// that removed a header removes it from x as well.
func mergeBack(x, cp *pluginapi.Exchange, original http.Header) {
	maps.Copy(x.Values, cp.Values)
	maps.Copy(x.Headers.Response, cp.Headers.Response)
	for k, v := range cp.Headers.Request {
		if slices.Equal(v, original[k]) {
			continue
		}
		if x.Headers.Request == nil {
			x.Headers.Request = http.Header{}
		}
		x.Headers.Request[k] = v
	}
	for k := range original {
		if _, still := cp.Headers.Request[k]; !still {
			delete(x.Headers.Request, k)
		}
	}
	if len(cp.Headers.Upstream) > 0 {
		if x.Headers.Upstream == nil {
			x.Headers.Upstream = http.Header{}
		}
		maps.Copy(x.Headers.Upstream, cp.Headers.Upstream)
	}
}
