package auditlog

import (
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
)

func TestFlushLiveUpdateCoalescesEnrichments(t *testing.T) {
	c, _ := echotest.Post(t, "/v1/chat/completions", nil)
	entry := &LogEntry{}
	publisher := &sessionUpdatePublisher{}
	c.Set(string(LogEntryKey), entry)
	c.Set(string(LogEntryLivePublisherKey), publisher)

	FlushLiveUpdate(c)
	require.Empty(t, publisher.events, "nothing changed yet")

	EnrichEntryWithAuthMethod(c, AuthMethodMasterKey)
	EnrichEntryWithUserPath(c, "/team")
	EnrichEntryWithRequestedModel(c, "gpt-4o-mini")
	EnrichEntryWithResolvedRoute(c, "openai/gpt-4o-mini", "openai", "openai")
	require.Empty(t, publisher.events, "enrichment alone does not publish")

	FlushLiveUpdate(c)
	assert.Equal(t, []string{LiveEventAuditUpdated}, publisher.events)

	FlushLiveUpdate(c)
	assert.Len(t, publisher.events, 1, "a second flush without changes does not publish")
}

func TestErrorEnrichmentPublishesPendingChangesAtOnce(t *testing.T) {
	c, _ := echotest.Post(t, "/v1/chat/completions", nil)
	entry := &LogEntry{}
	publisher := &sessionUpdatePublisher{}
	c.Set(string(LogEntryKey), entry)
	c.Set(string(LogEntryLivePublisherKey), publisher)

	EnrichEntryWithRequestedModel(c, "gpt-4o-mini")
	EnrichEntryWithError(c, "provider_error", "boom")
	require.Equal(t, []string{LiveEventAuditUpdated}, publisher.events)

	FlushLiveUpdate(c)
	assert.Len(t, publisher.events, 1, "the error event already carried the pending change")
}

func TestFlushLiveUpdateWithoutEntryOrPublisher(t *testing.T) {
	FlushLiveUpdate(nil)

	c, _ := echotest.Post(t, "/v1/chat/completions", nil)
	FlushLiveUpdate(c)

	entry := &LogEntry{}
	c.Set(string(LogEntryKey), entry)
	EnrichEntryWithRequestedModel(c, "gpt-4o-mini")
	FlushLiveUpdate(c)
	assert.Equal(t, "gpt-4o-mini", entry.RequestedModel)
}

func TestMiddlewareLeavesPendingChangesToTheWrite(t *testing.T) {
	logger := &captureLiveLogger{cfg: Config{Enabled: true}}
	c, _ := echotest.Post(t, "/v1/chat/completions", nil)

	handler := Middleware(logger)(func(c *echo.Context) error {
		EnrichEntryWithRequestedModel(c, "gpt-4o-mini")
		return nil
	})
	require.NoError(t, handler(c))

	require.Len(t, logger.events, 1)
	assert.Equal(t, LiveEventAuditStarted, logger.events[0].eventType)
	assert.Equal(t, 1, logger.writes, "the write publishes the completed entry")
}

func TestMiddlewareFlushesPendingChangesOfStreamedEntry(t *testing.T) {
	logger := &captureLiveLogger{cfg: Config{Enabled: true}}
	c, _ := echotest.Post(t, "/v1/chat/completions", nil)

	handler := Middleware(logger)(func(c *echo.Context) error {
		MarkEntryAsStreaming(c, true)
		EnrichEntryWithRequestedModel(c, "gpt-4o-mini")
		return nil
	})
	require.NoError(t, handler(c))

	require.Len(t, logger.events, 2)
	assert.Equal(t, LiveEventAuditUpdated, logger.events[1].eventType)
	assert.Zero(t, logger.writes, "the stream observer writes a streamed entry")
}
