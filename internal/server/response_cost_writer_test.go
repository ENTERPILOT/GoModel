package server

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/echotest"
)

// What the writer holds back is relayed however the handler ends: nothing of
// the handler's response is lost when it fails or panics half-way, a held
// piece is relayed as it was written, and a complete one is still cleaned.
func TestResponseCostMiddleware_RelaysHeldBytesHoweverTheHandlerEnds(t *testing.T) {
	failure := errors.New("upstream went away")
	endings := []struct {
		name string
		end  func() error
	}{
		{name: "returns", end: func() error { return nil }},
		{name: "fails", end: func() error { return failure }},
		{name: "panics", end: func() error { panic(failure) }},
	}
	bodies := []struct {
		name        string
		contentType string
		written     string
		held        string // the part the writer holds back until the handler ends
		wantHeld    string // what the client receives for it
	}{
		{
			name: "JSON cut inside its usage object", contentType: "application/json",
			written: `{"id":"x","usage":{"prompt_tokens":1,`, held: `{"prompt_tokens":1,`, wantHeld: `{"prompt_tokens":1,`,
		},
		{
			name: "stream cut inside an event", contentType: "text/event-stream",
			written: "data: {\"id\":\"x\"}\n\ndata: {\"usage\":{\"prompt_tok", held: "data: {\"usage\":{\"prompt_tok", wantHeld: "data: {\"usage\":{\"prompt_tok",
		},
		{
			name: "stream that ends without a blank line", contentType: "text/event-stream",
			written: "data: {\"id\":\"x\"}\n\ndata: {\"usage\":{\"prompt_tokens\":1,\"cost\":9}}", held: "data: {\"usage\":{\"prompt_tokens\":1,\"cost\":9}}", wantHeld: "data: {\"usage\":{\"prompt_tokens\":1}}",
		},
	}
	for _, body := range bodies {
		for _, ending := range endings {
			t.Run(body.name+"/handler "+ending.name, func(t *testing.T) {
				c, rec := echotest.Post(t, "/v1/chat/completions", nil)
				relayed := body.written[:len(body.written)-len(body.held)]
				handler := responseCostMiddleware(false)(func(c *echo.Context) error {
					c.Response().Header().Set("Content-Type", body.contentType)
					_, err := c.Response().Write([]byte(body.written))
					require.NoError(t, err)
					require.Equal(t, relayed, rec.Body.String(), "held back while the handler runs")
					return ending.end()
				})

				var err error
				if ending.name == "panics" {
					assert.PanicsWithValue(t, failure, func() { _ = handler(c) })
				} else {
					err = handler(c)
				}

				assert.Equal(t, ending.end != nil && ending.name == "fails", errors.Is(err, failure))
				assert.Equal(t, relayed+body.wantHeld, rec.Body.String())

				// Whatever answers the failure afterwards is not the
				// handler's response: it passes through as it is written.
				const after = `{"error":{"message":"boom"},"usage":{"cost":9}`
				_, writeErr := c.Response().Write([]byte(after))
				require.NoError(t, writeErr)
				assert.Equal(t, relayed+body.wantHeld+after, rec.Body.String())
			})
		}
	}
}

// A usage object over the limit is not held: it is relayed as it is, without
// a cost and with the members an upstream gave it, and the body after it is
// read on.
func TestJSONUsageEditor_UsageObjectOverTheLimit(t *testing.T) {
	const (
		large = `{"id":"x","usage":{"prompt_tokens":1,"cost":9,"padding":"0123456789"},"more":1`
		small = `,"usage":{"cost":8}}`
	)
	edit := newUsageCostEdit(0.5, true, true)
	for _, size := range []int{1, 3, len(large + small)} {
		editor := &jsonUsageEditor{edit: edit, maxUsage: 32}

		got := editBodyPieces(editor, pieces(large+small, size)...)

		assert.Equal(t, large+`,"usage":{"cost":0.5}}`, got, "write size %d", size)
		assert.LessOrEqual(t, cap(editor.usage), 64, "the oversized object's buffer is released")
	}

	within := &jsonUsageEditor{edit: edit, maxUsage: 64}
	assert.Equal(t, `{"id":"x","usage":{"prompt_tokens":1,"padding":"0123456789","cost":0.5},"more":1,"usage":{"cost":0.5}}`,
		editBodyPieces(within, large+small))
}

// The limit counts every byte of the object, its closing brace included: an
// object of exactly the limit is rewritten, and one byte more is relayed as
// it is. Either way the members after it are read on, so a provider's cost
// behind it is still removed.
func TestJSONUsageEditor_UsageObjectAtTheLimit(t *testing.T) {
	const (
		object = `{"prompt_tokens":1,"cost":9}`
		body   = `{"usage":` + object + `,"cost":7,"id":"x"}`
	)
	tests := []struct {
		name  string
		limit int
		want  string
	}{
		{name: "at the limit", limit: len(object), want: `{"usage":{"prompt_tokens":1,"cost":0.5},"id":"x"}`},
		{name: "one byte over", limit: len(object) - 1, want: `{"usage":` + object + `,"id":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, size := range []int{1, 3, len(body)} {
				editor := &jsonUsageEditor{edit: newUsageCostEdit(0.5, true, true), maxUsage: tt.limit}

				assert.Equal(t, tt.want, editBodyPieces(editor, pieces(body, size)...), "write size %d", size)
			}
		})
	}
}

// With markups the bytes between two top-level members are held until the
// second is named. An upstream cannot make the gateway hold a body's worth of
// them: whitespace past the bound is relayed as it arrives, the body stays
// the JSON it was, and a provider's cost is still removed with its comma.
func TestJSONUsageEditor_BoundsTheHeldSeparator(t *testing.T) {
	gap := strings.Repeat(" ", 16*maxHeldSeparatorBytes)
	tests := []struct {
		name string
		body string
		want string // the body without its whitespace
	}{
		{name: "before a member that stays", body: `{"id":"x",` + gap + `"usage":{"total_tokens":3}}`, want: `{"id":"x","usage":{"total_tokens":3,"cost":0.5}}`},
		{name: "before a removed member", body: `{"id":"x",` + gap + `"cost":9,"usage":{}}`, want: `{"id":"x","usage":{"cost":0.5}}`},
		{name: "before the comma of a removed member", body: `{"id":"x"` + gap + `,` + gap + `"cost":9}`, want: `{"id":"x"}`},
		{name: "after a removed first member", body: `{"cost":9,` + gap + `"id":"x"}`, want: `{"id":"x"}`},
		{name: "before the closing brace", body: `{"id":"x"` + gap + `}`, want: `{"id":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			whole := editedBody(newUsageCostEdit(0.5, true, true), tt.body)
			for _, size := range []int{1, maxHeldSeparatorBytes + 1, len(tt.body)} {
				editor := &jsonUsageEditor{edit: newUsageCostEdit(0.5, true, true)}
				held := 0
				var out bytes.Buffer
				for _, piece := range pieces(tt.body, size) {
					require.NoError(t, editor.feed(&out, []byte(piece)))
					held = max(held, cap(editor.members.held))
				}
				out.Write(editor.flush())

				require.True(t, gjson.ValidBytes(out.Bytes()), "write size %d", size)
				assert.Equal(t, tt.want, strings.ReplaceAll(out.String(), " ", ""), "write size %d", size)
				require.Equal(t, whole, out.String(), "every split gives the same body (write size %d)", size)
				assert.LessOrEqual(t, held, 2*maxHeldSeparatorBytes, "write size %d", size)
			}
		})
	}
}

// The limit a gateway runs with is the size of the largest event it reads.
func TestJSONUsageEditor_DefaultLimit(t *testing.T) {
	assert.Equal(t, 64<<20, maxHeldUsageBytes)
}

// flushRecorder counts the flushes that reach the connection.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes int
}

func (r *flushRecorder) Flush() { r.flushes++ }

// hijackRecorder is a connection that can be hijacked.
type hijackRecorder struct {
	*httptest.ResponseRecorder
	conn net.Conn
}

func (r *hijackRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.conn, nil, nil
}

// A flush reaches the connection at once, also while part of an event is held
// back, so the events before it are not delayed.
func TestResponseCostWriter_FlushPassesThrough(t *testing.T) {
	c, rec := echotest.Post(t, "/v1/chat/completions", nil)
	flushes := &flushRecorder{ResponseRecorder: rec}
	c.SetResponse(flushes)
	handler := responseCostMiddleware(false)(func(c *echo.Context) error {
		c.Response().Header().Set("Content-Type", "text/event-stream")
		_, err := c.Response().Write([]byte("data: {\"id\":\"x\"}\n\ndata: {\"usage\""))
		require.NoError(t, err)

		require.NoError(t, http.NewResponseController(c.Response()).Flush())

		assert.Equal(t, 1, flushes.flushes)
		assert.Equal(t, "data: {\"id\":\"x\"}\n\n", rec.Body.String())
		return nil
	})

	require.NoError(t, handler(c))

	assert.Equal(t, "data: {\"id\":\"x\"}\n\ndata: {\"usage\"", rec.Body.String())
	assert.Equal(t, 2, flushes.flushes, "what the stream ended with is flushed too")
}

// A handler that takes over the connection (a WebSocket upgrade) gets the
// connection beneath the writer, or the error of a connection that has none.
func TestResponseCostWriter_HijackPassesThrough(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	tests := []struct {
		name     string
		wrap     func(*httptest.ResponseRecorder) http.ResponseWriter
		wantConn net.Conn
		wantErr  error
	}{
		{
			name:     "hijackable connection",
			wrap:     func(rec *httptest.ResponseRecorder) http.ResponseWriter { return &hijackRecorder{rec, server} },
			wantConn: server,
		},
		{
			name:    "connection that cannot be hijacked",
			wrap:    func(rec *httptest.ResponseRecorder) http.ResponseWriter { return rec },
			wantErr: http.ErrNotSupported,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := echotest.Get(t, "/v1/realtime")
			c.SetResponse(tt.wrap(rec))
			handler := responseCostMiddleware(false)(func(c *echo.Context) error {
				conn, _, err := http.NewResponseController(c.Response()).Hijack()

				require.ErrorIs(t, err, tt.wantErr)
				assert.Equal(t, tt.wantConn, conn)
				return nil
			})

			require.NoError(t, handler(c))

			assert.Empty(t, rec.Body.String())
		})
	}
}
