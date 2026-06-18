package admin

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
	"gomodel/internal/live"
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

	if sub.Reset {
		if err := writeLiveEvent(res, live.Event{
			Seq:  h.liveBroker.LatestSeq(),
			Type: live.EventReset,
		}); err != nil {
			return err
		}
	}
	for _, event := range sub.Replay {
		if !filter.matches(event.Type) {
			continue
		}
		if err := writeLiveEvent(res, event); err != nil {
			return err
		}
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
			if !filter.matches(event.Type) {
				continue
			}
			if err := writeLiveEvent(res, event); err != nil {
				return err
			}
		case <-ticker.C:
			if err := writeLiveEvent(res, live.Event{
				Seq:  h.liveBroker.LatestSeq(),
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
	for _, item := range strings.Split(raw, ",") {
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

func writeLiveEvent(res http.ResponseWriter, event live.Event) error {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if event.Seq > 0 {
		if _, err := fmt.Fprintf(res, "id: %d\n", event.Seq); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(res, "event: %s\n", event.Type); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(res, "data: %s\n\n", payload); err != nil {
		return err
	}
	if flusher, ok := res.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}
