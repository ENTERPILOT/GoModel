package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/live"
)

// LiveLogs handles GET /admin/live/logs.
func (h *Handler) LiveLogs(c *echo.Context) error {
	if h.liveBroker == nil || !h.liveBroker.Enabled() {
		return handleError(c, featureUnavailableError("live logs are unavailable"))
	}

	cursor, err := liveCursor(c.QueryParam("cursor"))
	if err != nil {
		return handleError(c, err)
	}
	filter := liveTypeFilter(c.QueryParam("types"))
	sub := h.liveBroker.Subscribe(cursor)
	if sub == nil {
		return handleError(c, featureUnavailableError("live logs are unavailable"))
	}
	defer sub.Close()

	res := c.Response()
	// SSE responses are intentionally long-lived; keep disconnect detection via writes.
	_ = http.NewResponseController(res).SetWriteDeadline(time.Time{})
	res.Header().Set(echo.HeaderContentType, "text/event-stream")
	res.Header().Set(echo.HeaderCacheControl, "no-cache, no-transform")
	res.Header().Set(echo.HeaderConnection, "keep-alive")
	res.Header().Set("X-Accel-Buffering", "no")
	res.WriteHeader(http.StatusOK)

	// lastSeq is the newest sequence this connection has seen (delivered or
	// deliberately filtered). Heartbeats advertise it as the reconnect
	// cursor, so it must never run ahead of the events on the wire: the
	// broker-global counter can, when an event published after subscribe is
	// still queued on sub.Events.
	lastSeq := sub.Latest
	stream := liveEventWriter{res: res}

	if sub.Reset {
		if err := stream.write(live.Event{
			Seq:  lastSeq,
			Type: live.EventReset,
		}); err != nil {
			return err
		}
	}
	for _, event := range sub.Replay {
		if !filter.matches(event.Type) {
			continue
		}
		if err := stream.write(event); err != nil {
			return err
		}
	}

	// An immediate heartbeat flushes the headers and gives the client its
	// cursor right away; without it nothing reaches the wire until the first
	// ticker heartbeat, so a freshly opened stream looked pending for up to
	// the whole heartbeat interval.
	if err := stream.write(live.Event{
		Seq:  lastSeq,
		Type: live.EventHeartbeat,
	}); err != nil {
		return err
	}

	ticker := time.NewTicker(h.liveBroker.Heartbeat())
	defer ticker.Stop()

	ctx := c.Request().Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-sub.Events:
			if !ok {
				return nil
			}
			if event.Seq > lastSeq {
				lastSeq = event.Seq
			}
			if !filter.matches(event.Type) {
				continue
			}
			if err := stream.write(event); err != nil {
				return err
			}
		case <-ticker.C:
			if err := stream.write(live.Event{
				Seq:  lastSeq,
				Type: live.EventHeartbeat,
			}); err != nil {
				return err
			}
		}
	}
}

func liveCursor(raw string) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, core.NewInvalidRequestError("invalid cursor, expected non-negative integer", err)
	}
	return cursor, nil
}

type liveLogTypeFilter struct {
	provided bool
	types    map[string]struct{}
}

func liveTypeFilter(raw string) liveLogTypeFilter {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return liveLogTypeFilter{}
	}
	filter := liveLogTypeFilter{
		provided: true,
		types:    map[string]struct{}{},
	}
	for item := range strings.SplitSeq(raw, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		switch item {
		case "audit", "usage":
			filter.types[item] = struct{}{}
		}
	}
	return filter
}

func (f liveLogTypeFilter) matches(eventType string) bool {
	if !f.provided {
		return true
	}
	if len(f.types) == 0 {
		return false
	}
	prefix, _, ok := strings.Cut(eventType, ".")
	if !ok {
		prefix = eventType
	}
	_, matched := f.types[prefix]
	return matched
}

// maxReusedLiveFrameBuffer caps the frame buffer a connection keeps between
// writes. Audit events can carry bodies near 1 MiB; keeping such a buffer would
// pin it for the whole life of a dashboard connection that otherwise sends
// small frames and heartbeats.
const maxReusedLiveFrameBuffer = 64 << 10

// liveEventWriter writes live events as SSE frames, reusing one buffer for
// every frame of the connection up to maxReusedLiveFrameBuffer.
type liveEventWriter struct {
	res http.ResponseWriter
	buf []byte
}

func (w *liveEventWriter) write(event live.Event) error {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	buf, err := appendLiveEventFrame(w.buf[:0], event)
	if err != nil {
		return err
	}
	if cap(buf) <= maxReusedLiveFrameBuffer {
		w.buf = buf
	} else {
		w.buf = nil
	}
	if _, err := w.res.Write(buf); err != nil {
		return err
	}
	if flusher, ok := w.res.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

// appendLiveEventFrame appends the SSE frame for event to dst. The envelope is
// encoded by hand, byte for byte what json.Marshal(event) produces: Data is
// json.Marshal output the broker shares across subscribers, already compact
// and escaped, which marshaling the envelope per subscriber re-scanned and
// copied each time.
func appendLiveEventFrame(dst []byte, event live.Event) ([]byte, error) {
	if event.Seq > 0 {
		dst = append(dst, "id: "...)
		dst = strconv.AppendUint(dst, event.Seq, 10)
		dst = append(dst, '\n')
	}
	dst = append(dst, "event: "...)
	dst = append(dst, event.Type...)
	dst = append(dst, "\ndata: {\"seq\":"...)
	dst = strconv.AppendUint(dst, event.Seq, 10)
	dst = append(dst, `,"type":`...)
	dst, err := appendLiveJSONString(dst, event.Type)
	if err != nil {
		return dst, err
	}
	if event.RequestID != "" {
		dst = append(dst, `,"request_id":`...)
		if dst, err = appendLiveJSONString(dst, event.RequestID); err != nil {
			return dst, err
		}
	}
	dst = append(dst, `,"timestamp":"`...)
	if dst, err = event.Timestamp.AppendText(dst); err != nil {
		return dst, err
	}
	dst = append(dst, '"')
	if len(event.Data) > 0 {
		dst = append(dst, `,"data":`...)
		dst = append(dst, event.Data...)
	}
	return append(dst, "}\n\n"...), nil
}

// appendLiveJSONString appends s as a JSON string. Event types and request IDs
// are plain ASCII in practice and are copied as is; anything json.Marshal
// would escape takes json.Marshal.
func appendLiveJSONString(dst []byte, s string) ([]byte, error) {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c > 0x7e || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			encoded, err := json.Marshal(s)
			if err != nil {
				return dst, err
			}
			return append(dst, encoded...), nil
		}
	}
	dst = append(dst, '"')
	dst = append(dst, s...)
	return append(dst, '"'), nil
}
