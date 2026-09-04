package plugins

import (
	"context"
	"errors"
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
// run concurrently, each on a shallow copy of the exchange (their Values and
// header edits are merged back afterwards), then the mutating one runs on
// the exchange itself. The first blocking decision ends the chain after its
// step completes.
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
			record, err := c.invoke(ctx, mutator, x, call, true)
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

// runReaders runs the readers concurrently on copies of x. A reader the
// runtime stopped waiting for (see ErrAbandoned) may still be writing its
// copy, so that copy is dropped instead of merged.
func (c *Chain) runReaders(ctx context.Context, readers []*Instance, x *pluginapi.Exchange, call hookCall) ([]Record, error) {
	if len(readers) == 0 {
		return nil, nil
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
			records[i], errs[i] = c.invoke(ctx, inst, xi, call, false)
		}(i, inst, copies[i])
	}
	wg.Wait()
	for i, xi := range copies {
		if errors.Is(records[i].Err, ErrAbandoned) {
			continue
		}
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
// logged and recorded with an allow decision. shared says the instance ran
// on the chain's own exchange, so an abandoned call cannot fail open: the
// hook may still be editing what the next step would read.
func (c *Chain) invoke(ctx context.Context, inst *Instance, x *pluginapi.Exchange, call hookCall, shared bool) (Record, error) {
	start := time.Now()
	decision, err := callHook(ctx, inst, x, call)
	record := Record{Instance: inst.Name, Decision: NormalizeDecision(decision), Duration: time.Since(start), Err: err}
	if err == nil {
		return record, nil
	}
	record.Decision = pluginapi.Allow()
	if inst.FailsOpen(c.Phase, err, shared) {
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

// ErrAbandoned marks a hook call the runtime stopped waiting for because the
// instance timeout expired or the request ended. The hook may still be
// running: it received a cancelled context and is expected to return soon,
// but nothing it does from then on reaches the request.
var ErrAbandoned = errors.New("plugin call abandoned")

// Call runs fn under the instance's timeout with panic recovery. It returns
// when fn returns or when ctx ends, whichever comes first, so a hook that
// ignores its context bounds neither request latency nor shutdown. A call
// that returns after its deadline is reported as abandoned as well.
func Call[T any](ctx context.Context, inst *Instance, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if ctx == nil {
		ctx = context.Background()
	}
	if inst != nil && inst.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, inst.Timeout)
		defer cancel()
	}
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- result{err: fmt.Errorf("plugin panicked: %v", r)}
			}
		}()
		value, err := fn(ctx)
		done <- result{value: value, err: err}
	}()
	select {
	case res := <-done:
		if res.err == nil && ctx.Err() != nil {
			return zero, abandoned(inst, ctx.Err())
		}
		return res.value, res.err
	case <-ctx.Done():
		return zero, abandoned(inst, ctx.Err())
	}
}

func abandoned(inst *Instance, cause error) error {
	if inst != nil && inst.Timeout > 0 && errors.Is(cause, context.DeadlineExceeded) {
		return fmt.Errorf("%w: exceeded its %s timeout", ErrAbandoned, inst.Timeout)
	}
	return fmt.Errorf("%w: %v", ErrAbandoned, cause)
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
