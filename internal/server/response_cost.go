package server

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

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
// request to its caller, in the shape OpenRouter uses: usage.cost, a number in
// USD, in every usage object the response carries. Whatever records the
// request's usage reports the cost on the context (usage.ReportCost). A
// buffered response also carries it in the X-GoModel-Cost headers, for the
// responses without a usage object (audio, most images). billed selects the
// billed cost over the provider cost, and removes usage.cost_details.
func responseCostMiddleware(billed bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			desc := core.DescribeEndpointPath(c.Request().URL.Path)
			if !desc.ModelInteraction {
				return next(c)
			}
			ctx, report := usage.WithCostReport(c.Request().Context(), billed)
			c.SetRequest(c.Request().WithContext(ctx))
			// Wrapped here, beneath the capture the response cache adds in the
			// handler, so a cached response never holds another request's cost.
			w := &responseCostWriter{
				ResponseWriter: c.Response(),
				report:         report,
				billed:         billed,
				editsJSON:      editsJSONBodies(desc.Operation),
			}
			c.SetResponse(w)
			err := next(c)
			w.finish()
			return err
		}
	}
}

// editsJSONBodies reports whether the JSON answers of an operation get the
// cost in their usage. A batch's usage, a file and a stored conversation are
// not the usage of the request that reads them.
func editsJSONBodies(op core.Operation) bool {
	switch op {
	case core.OperationBatches, core.OperationFiles, core.OperationConversations:
		return false
	}
	return true
}

// responseCostWriter adds the reported cost to the response as it is written.
type responseCostWriter struct {
	http.ResponseWriter
	report    *usage.CostReport
	billed    bool
	editsJSON bool
	prepared  bool
	// events splits an event stream so the events carrying usage can be
	// rewritten; body rewrites the usage of a JSON body. Both are nil for
	// every other response.
	events *streaming.EventScanner
	body   *jsonUsageEditor
	out    []byte
}

// prepare runs once, just before the response starts. The cost headers belong
// to this gateway: a value relayed from an upstream is dropped, and a
// buffered response carries this gateway's cost when it is known. A stream
// carries it in its events only.
func (w *responseCostWriter) prepare() {
	if w.prepared {
		return
	}
	w.prepared = true
	header := w.Header()
	deleteHeaderFold(header, responseCostHeader)
	deleteHeaderFold(header, responseCostCurrencyHeader)
	// An encoded body cannot be read; the gateway sends none.
	readable := header.Get("Content-Encoding") == ""
	if isSSEContentType(header) {
		if readable {
			w.events = &streaming.EventScanner{MaxEventBytes: streaming.MaxObservedEventBytes}
		}
		return
	}
	amount, known := w.report.Cost()
	if known {
		header.Set(responseCostHeader, formatResponseCost(amount))
		header.Set(responseCostCurrencyHeader, usage.ResponseCostCurrency)
	}
	if readable && w.editsJSON && isJSONContentType(header) {
		// The body changes length as it is rewritten.
		deleteHeaderFold(header, "Content-Length")
		w.body = &jsonUsageEditor{edit: newUsageCostEdit(amount, known, w.billed)}
	}
}

func (w *responseCostWriter) WriteHeader(code int) {
	w.prepare()
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseCostWriter) Write(p []byte) (int, error) {
	w.prepare()
	switch {
	case w.body != nil:
		if out := w.body.feed(p); len(out) > 0 {
			if _, err := w.ResponseWriter.Write(out); err != nil {
				return 0, err
			}
		}
		return len(p), nil
	case w.events == nil:
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

// finish relays what the body ended with: the unterminated event a stream may
// have ended with, or an unterminated usage object of a truncated JSON body.
func (w *responseCostWriter) finish() {
	w.out = w.out[:0]
	switch {
	case w.body != nil:
		w.out = append(w.out, w.body.flush()...)
	case w.events != nil:
		for _, ev := range w.events.Flush() {
			w.out = w.appendEvent(w.out, ev)
		}
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

// paceForResponseCost paces a stream one event per read when its response
// returns the cost, so the usage observer has priced exactly the events
// relayed so far when each one is written: an event's cost then matches the
// token counts beside it even when several arrived in one upstream read.
func paceForResponseCost(c *echo.Context, stream io.ReadCloser) io.ReadCloser {
	if usage.CostReportFromContext(c.Request().Context()) == nil {
		return stream
	}
	return streaming.NewEventPacedStream(stream)
}

// deleteHeaderFold removes every value of the header name, under any key that
// names it: Header.Del only removes the canonical key, and the server writes
// a key set in another case, or with stray spaces, as it is.
func deleteHeaderFold(header http.Header, name string) {
	for key := range header {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			delete(header, key)
		}
	}
}

// responseCostDecimals is the precision a returned cost is rounded to.
const responseCostDecimals = 12

// formatResponseCost renders a cost as a plain decimal JSON number rounded to
// 12 decimal places, without exponent or float noise, and without trailing
// zeros: 0.00002 * 1.5 reads 0.00003, 1e-9 reads 0.000000001.
func formatResponseCost(amount float64) string {
	if amount == 0 {
		return "0" // never -0
	}
	s := strconv.FormatFloat(amount, 'f', responseCostDecimals, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
