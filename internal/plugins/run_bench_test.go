package plugins

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/pluginapi"
)

// BenchmarkCallHook measures one hook call. The stream path runs this for
// every in-flight instance on every SSE event.
func BenchmarkCallHook(b *testing.B) {
	fn := func(ctx context.Context) (pluginapi.StreamDecision, error) {
		return pluginapi.StreamDecision{}, nil
	}

	b.Run("no_timeout", func(b *testing.B) {
		inst := &Instance{Name: "bench"}
		b.ReportAllocs()
		for b.Loop() {
			if _, err := Call(context.Background(), inst, fn); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("with_timeout", func(b *testing.B) {
		inst := &Instance{Name: "bench", Timeout: time.Minute}
		b.ReportAllocs()
		for b.Loop() {
			if _, err := Call(context.Background(), inst, fn); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkReportOutcomeNoStrategies measures the per-upstream-attempt cost
// when no routing-strategy plugin is configured, which is the default.
func BenchmarkReportOutcomeNoStrategies(b *testing.B) {
	r := &RouteResolver{}
	outcome := pluginapi.RouteOutcome{}
	b.ReportAllocs()
	for b.Loop() {
		r.ReportOutcome(outcome)
	}
}

// BenchmarkRunReadersSingle measures a chain step with one reader, the
// common shape. The step itself runs inline: what the number still covers is
// the exchange copy, the merge back, and the goroutine Call keeps to bound a
// hook that ignores its context.
func BenchmarkRunReadersSingle(b *testing.B) {
	c := &Chain{}
	readers := []*Instance{{Name: "reader"}}
	call := func(ctx context.Context, inst *Instance, x *pluginapi.Exchange) (pluginapi.Decision, error) {
		return pluginapi.Decision{}, nil
	}
	// Built once: a no-op reader leaves it unchanged, so the numbers are the
	// run's own cost rather than the fixture's.
	x := &pluginapi.Exchange{
		Headers: &pluginapi.Headers{
			Request:  http.Header{"Authorization": []string{"[REDACTED]"}, "Content-Type": []string{"application/json"}, "X-Trace": []string{"trace-1"}},
			Response: http.Header{},
		},
		Values: pluginapi.Values{},
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.runReaders(context.Background(), readers, x, call); err != nil {
			b.Fatal(err)
		}
	}
}
