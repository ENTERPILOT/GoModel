package llmclient

import "context"

// JoinHooks composes several hook sets into one. OnRequestStart callbacks run
// in order, threading the context through; OnRequestEnd callbacks run in
// order. Hook sets with nil callbacks are skipped.
func JoinHooks(hooks ...Hooks) Hooks {
	var starts []func(ctx context.Context, info RequestInfo) context.Context
	var ends []func(ctx context.Context, info ResponseInfo)
	for _, h := range hooks {
		if h.OnRequestStart != nil {
			starts = append(starts, h.OnRequestStart)
		}
		if h.OnRequestEnd != nil {
			ends = append(ends, h.OnRequestEnd)
		}
	}

	joined := Hooks{}
	if len(starts) == 1 {
		joined.OnRequestStart = starts[0]
	} else if len(starts) > 1 {
		joined.OnRequestStart = func(ctx context.Context, info RequestInfo) context.Context {
			for _, start := range starts {
				ctx = start(ctx, info)
			}
			return ctx
		}
	}
	if len(ends) == 1 {
		joined.OnRequestEnd = ends[0]
	} else if len(ends) > 1 {
		joined.OnRequestEnd = func(ctx context.Context, info ResponseInfo) {
			for _, end := range ends {
				end(ctx, info)
			}
		}
	}
	return joined
}
