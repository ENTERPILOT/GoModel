package server

import (
	"bufio"
	"net"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/streaming"
	"github.com/enterpilot/gomodel/internal/usage"
)

// Response headers carrying the cost GoModel calculated for a request.
const (
	responseCostHeader         = "X-GoModel-Cost"
	responseCostCurrencyHeader = "X-GoModel-Cost-Currency"
)

// responseCostMiddleware returns the cost GoModel calculated for a model
// request to its caller. Whatever records the request's usage reports the
// cost on the context (usage.ReportCost); the response then carries it in the
// X-GoModel-Cost headers when it is known before the response starts, and in
// the usage of a stream's events otherwise. billed selects the billed cost
// over the provider cost.
func responseCostMiddleware(billed bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if !core.IsModelInteractionPath(c.Request().URL.Path) {
				return next(c)
			}
			ctx, report := usage.WithCostReport(c.Request().Context(), billed)
			c.SetRequest(c.Request().WithContext(ctx))
			// Wrapped here, beneath the capture the response cache adds in the
			// handler, so a cached response never holds another request's cost.
			w := &responseCostWriter{ResponseWriter: c.Response(), report: report}
			c.SetResponse(w)
			err := next(c)
			w.finish()
			return err
		}
	}
}

// responseCostWriter adds the reported cost to the response as it is written.
type responseCostWriter struct {
	http.ResponseWriter
	report   *usage.CostReport
	prepared bool
	// events splits an event stream so the events carrying usage can be
	// rewritten; nil for every other response.
	events *streaming.EventScanner
	out    []byte
}

// prepare runs once, just before the response starts. The cost headers belong
// to this gateway: a value relayed from an upstream is replaced, or dropped
// when the cost is unknown.
func (w *responseCostWriter) prepare() {
	if w.prepared {
		return
	}
	w.prepared = true
	header := w.Header()
	header.Del(responseCostHeader)
	header.Del(responseCostCurrencyHeader)
	if amount, ok := w.report.Cost(); ok {
		header.Set(responseCostHeader, formatResponseCost(amount))
		header.Set(responseCostCurrencyHeader, usage.ResponseCostCurrency)
	}
	// An encoded body cannot be split into events; the gateway sends none.
	if isSSEContentType(header) && header.Get("Content-Encoding") == "" {
		w.events = &streaming.EventScanner{MaxEventBytes: streaming.MaxObservedEventBytes}
	}
}

func (w *responseCostWriter) WriteHeader(code int) {
	w.prepare()
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseCostWriter) Write(p []byte) (int, error) {
	w.prepare()
	if w.events == nil {
		return w.ResponseWriter.Write(p)
	}
	// Only whole events are relayed; a client cannot act on part of one.
	w.out = w.out[:0]
	for _, ev := range w.events.Feed(p) {
		w.out = w.appendEvent(w.out, ev)
	}
	if len(w.out) > 0 {
		if _, err := w.ResponseWriter.Write(w.out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// finish relays the unterminated event a stream may have ended with.
func (w *responseCostWriter) finish() {
	if w.events == nil {
		return
	}
	w.out = w.out[:0]
	for _, ev := range w.events.Flush() {
		w.out = w.appendEvent(w.out, ev)
	}
	if len(w.out) > 0 {
		if _, err := w.ResponseWriter.Write(w.out); err == nil {
			w.Flush()
		}
	}
}

func (w *responseCostWriter) Flush() {
	w.prepare()
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseCostWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *responseCostWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// formatResponseCost renders a cost as a plain decimal with every digit the
// calculation produced: request costs are small fractions of a dollar.
func formatResponseCost(amount float64) string {
	return strconv.FormatFloat(amount, 'f', -1, 64)
}
