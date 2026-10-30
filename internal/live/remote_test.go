package live

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/usage"
)

const (
	testSelf    = "replica-a"
	testPeer    = "replica-b"
	testTimeout = 5 * time.Second
)

var testT0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// fakeRow is one stamped row of a fake audit or usage table.
type fakeRow struct {
	id        string
	writer    string
	writtenAt int64
	timestamp time.Time
}

// fakeTable stands in for the audit and usage tables: RecentWrites filters
// and orders like the stores do.
type fakeTable struct {
	mu          sync.Mutex
	rows        []fakeRow
	recentCalls int
	failLoads   int
}

func (f *fakeTable) add(id, writer string, writtenAt time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, fakeRow{id: id, writer: writer, writtenAt: writtenAt.UnixMicro(), timestamp: writtenAt})
}

func (f *fakeTable) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recentCalls
}

func (f *fakeTable) RecentWrites(_ context.Context, since int64, excludeWriter string, limit int) ([]storage.RecentWrite, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recentCalls++
	var writes []storage.RecentWrite
	for _, row := range f.rows {
		if row.writer != "" && row.writer != excludeWriter && row.writtenAt >= since {
			writes = append(writes, storage.RecentWrite{ID: row.id, WrittenAt: row.writtenAt})
		}
	}
	sort.SliceStable(writes, func(i, j int) bool { return writes[i].WrittenAt > writes[j].WrittenAt })
	if len(writes) > limit {
		writes = writes[:limit]
	}
	return writes, nil
}

func (f *fakeTable) load(ids []string) ([]fakeRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failLoads > 0 {
		f.failLoads--
		return nil, errors.New("database unavailable")
	}
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	var rows []fakeRow
	for _, row := range f.rows {
		if wanted[row.id] {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

type fakeAuditTable struct{ fakeTable }

func (f *fakeAuditTable) EntriesByID(_ context.Context, ids []string) ([]auditlog.LogEntry, error) {
	rows, err := f.load(ids)
	if err != nil {
		return nil, err
	}
	entries := make([]auditlog.LogEntry, len(rows))
	for i, row := range rows {
		entries[i] = auditlog.LogEntry{
			ID:         row.id,
			RequestID:  "req-" + row.id,
			Timestamp:  row.timestamp,
			StatusCode: 200,
			Data: &auditlog.LogData{
				RequestBody: auditlog.CaptureLoggedBody([]byte(`{"model":"gpt-test"}`)),
			},
		}
	}
	return entries, nil
}

type fakeUsageTable struct{ fakeTable }

func (f *fakeUsageTable) LogEntriesByID(_ context.Context, ids []string) ([]usage.UsageLogEntry, error) {
	rows, err := f.load(ids)
	if err != nil {
		return nil, err
	}
	entries := make([]usage.UsageLogEntry, len(rows))
	for i, row := range rows {
		entries[i] = usage.UsageLogEntry{
			ID:          row.id,
			RequestID:   "req-" + row.id,
			Timestamp:   row.timestamp,
			TotalTokens: 15,
		}
	}
	return entries, nil
}

// testClock is the feed's injectable clock.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// testHarness drives one broker's feed: ticks are delivered by hand, and
// every finished poll is reported on polls.
type testHarness struct {
	broker  *Broker
	feed    *remoteFeed
	ticks   chan time.Time
	created chan struct{}
	stopped chan struct{}
	polls   chan struct{}
}

func newTestHarness(t *testing.T, writer string, audit auditlog.RecentWriteReader, usageReader usage.RecentWriteReader, clock *testClock) *testHarness {
	t.Helper()
	h := &testHarness{
		broker:  NewBroker(Config{Enabled: true}),
		ticks:   make(chan time.Time),
		created: make(chan struct{}, 16),
		stopped: make(chan struct{}, 16),
		polls:   make(chan struct{}, 1024),
	}
	h.feed = newRemoteFeed(h.broker, RemoteConfig{Writer: writer, Audit: audit, Usage: usageReader})
	h.feed.now = clock.Now
	h.feed.newTicker = func(time.Duration) (<-chan time.Time, func()) {
		h.created <- struct{}{}
		return h.ticks, func() { h.stopped <- struct{}{} }
	}
	h.feed.polled = func() { h.polls <- struct{}{} }
	h.broker.startRemote(h.feed)
	t.Cleanup(h.broker.Close)
	return h
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(testTimeout):
		require.FailNow(t, "timed out waiting for "+what)
	}
}

// subscribe connects a dashboard and waits for the poll it starts.
func (h *testHarness) subscribe(t *testing.T) *Subscription {
	t.Helper()
	sub := h.broker.Subscribe(0)
	require.NotNil(t, sub)
	waitSignal(t, h.created, "the feed to start polling")
	waitSignal(t, h.polls, "the first poll")
	return sub
}

// unsubscribe disconnects the last dashboard and waits for polling to stop.
func (h *testHarness) unsubscribe(t *testing.T, sub *Subscription) {
	t.Helper()
	sub.Close()
	waitSignal(t, h.stopped, "the feed to stop polling")
}

// tick runs one poll and waits for it to finish.
func (h *testHarness) tick(t *testing.T) {
	t.Helper()
	select {
	case h.ticks <- time.Time{}:
	case <-time.After(testTimeout):
		require.FailNow(t, "the feed is not polling")
	}
	waitSignal(t, h.polls, "a poll")
}

// pendingEvents drains what a finished poll published: publishing is
// synchronous, so after the poll reported done everything is queued.
func pendingEvents(sub *Subscription) []Event {
	var events []Event
	for {
		select {
		case event := <-sub.Events:
			events = append(events, event)
		default:
			return events
		}
	}
}

func eventIDs(t *testing.T, events []Event) []string {
	t.Helper()
	ids := make([]string, len(events))
	for i, event := range events {
		var data struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal(event.Data, &data))
		ids[i] = event.Type + ":" + data.ID
	}
	return ids
}

func TestRemoteFeedPollsOnlyWhileSubscribed(t *testing.T) {
	audit := &fakeAuditTable{}
	h := newTestHarness(t, testSelf, audit, nil, &testClock{now: testT0})

	assert.Zero(t, audit.calls(), "nothing is read before a dashboard connects")

	sub := h.subscribe(t)
	assert.Equal(t, 1, audit.calls(), "a new subscriber is caught up at once")
	h.tick(t)
	assert.Equal(t, 2, audit.calls())

	h.unsubscribe(t, sub)
	select {
	case h.ticks <- time.Time{}:
		require.FailNow(t, "the feed still polls without a subscriber")
	default:
	}
	assert.Equal(t, 2, audit.calls(), "no query once the last subscriber left")

	sub = h.subscribe(t)
	assert.Equal(t, 3, audit.calls(), "polling resumes with the next subscriber")
	h.tick(t)
	assert.Equal(t, 4, audit.calls())

	h.broker.Close()
	waitSignal(t, h.stopped, "Close to stop the feed")
	_, ok := <-sub.Events
	assert.False(t, ok)
}

func TestRemoteFeedPublishesEachRemoteEntryOnce(t *testing.T) {
	audit := &fakeAuditTable{}
	clock := &testClock{now: testT0}
	audit.add("remote-1", testPeer, testT0.Add(-time.Second))
	audit.add("local-1", testSelf, testT0.Add(-time.Second))
	h := newTestHarness(t, testSelf, audit, nil, clock)

	sub := h.subscribe(t)
	events := pendingEvents(sub)
	require.Equal(t, []string{EventAuditFlushed + ":remote-1"}, eventIDs(t, events),
		"the peer's entry is published, this replica's own is not")
	assert.Equal(t, "req-remote-1", events[0].RequestID)

	h.tick(t)
	assert.Empty(t, pendingEvents(sub), "a later poll does not publish it again")

	clock.Advance(time.Second)
	audit.add("remote-2", "replica-c", clock.Now())
	h.tick(t)
	assert.Equal(t, []string{EventAuditFlushed + ":remote-2"}, eventIDs(t, pendingEvents(sub)))

	// A batch stamped before the one already read, but visible only now.
	audit.add("remote-late", testPeer, clock.Now().Add(-5*time.Second))
	h.tick(t)
	assert.Equal(t, []string{EventAuditFlushed + ":remote-late"}, eventIDs(t, pendingEvents(sub)),
		"an entry that becomes visible late is still published")

	clock.Advance(remoteWindow + time.Second)
	h.tick(t)
	assert.Empty(t, pendingEvents(sub))
	assert.Empty(t, h.feed.auditSeen.ids, "entries that left the window are forgotten")
}

func TestRemoteFeedStartsAtWindowAndRemembersAcrossIdle(t *testing.T) {
	audit := &fakeAuditTable{}
	clock := &testClock{now: testT0}
	audit.add("too-old", testPeer, testT0.Add(-remoteWindow-time.Second))
	audit.add("recent", testPeer, testT0.Add(-time.Second))
	h := newTestHarness(t, testSelf, audit, nil, clock)

	sub := h.subscribe(t)
	assert.Equal(t, []string{EventAuditFlushed + ":recent"}, eventIDs(t, pendingEvents(sub)),
		"a new subscriber gets only the window; older entries are history")
	h.unsubscribe(t, sub)

	clock.Advance(3 * time.Second)
	audit.add("while-idle", testPeer, clock.Now().Add(-time.Second))
	sub = h.subscribe(t)
	assert.Equal(t, []string{EventAuditFlushed + ":while-idle"}, eventIDs(t, pendingEvents(sub)),
		"after an idle spell only what is new is published")
	h.unsubscribe(t, sub)

	// A restarted replica starts a new stream, which dashboards reset to,
	// and catches it up on the window once.
	restarted := newTestHarness(t, testSelf, audit, nil, clock)
	sub = restarted.subscribe(t)
	assert.Equal(t, []string{EventAuditFlushed + ":recent", EventAuditFlushed + ":while-idle"},
		eventIDs(t, pendingEvents(sub)), "oldest first")
	restarted.tick(t)
	assert.Empty(t, pendingEvents(sub))
}

func TestRemoteFeedRetriesAFailedLoad(t *testing.T) {
	audit := &fakeAuditTable{}
	audit.failLoads = 1
	audit.add("remote-1", testPeer, testT0.Add(-time.Second))
	h := newTestHarness(t, testSelf, audit, nil, &testClock{now: testT0})

	sub := h.subscribe(t)
	assert.Empty(t, pendingEvents(sub))
	h.tick(t)
	assert.Equal(t, []string{EventAuditFlushed + ":remote-1"}, eventIDs(t, pendingEvents(sub)))
}

func TestRemoteFeedCapsEntriesPerPoll(t *testing.T) {
	audit := &fakeAuditTable{}
	const extra = 5
	for i := range remoteEntryLimit + extra {
		audit.add(fmt.Sprintf("remote-%03d", i), testPeer, testT0.Add(-time.Second+time.Duration(i)*time.Microsecond))
	}
	h := newTestHarness(t, testSelf, audit, nil, &testClock{now: testT0})

	sub := h.subscribe(t)
	events := pendingEvents(sub)
	require.Len(t, events, remoteEntryLimit)
	ids := eventIDs(t, events)
	assert.Equal(t, fmt.Sprintf("%s:remote-%03d", EventAuditFlushed, extra), ids[0], "the oldest are skipped")
	assert.Equal(t, fmt.Sprintf("%s:remote-%03d", EventAuditFlushed, remoteEntryLimit+extra-1), ids[len(ids)-1])

	h.tick(t)
	assert.Empty(t, pendingEvents(sub), "skipped entries are not published late")
}

func TestRemoteFeedPublishesAuditThenUsage(t *testing.T) {
	audit := &fakeAuditTable{}
	usageTable := &fakeUsageTable{}
	audit.add("audit-1", testPeer, testT0.Add(-time.Second))
	usageTable.add("usage-1", testPeer, testT0.Add(-time.Second))
	h := newTestHarness(t, testSelf, audit, usageTable, &testClock{now: testT0})

	sub := h.subscribe(t)
	events := pendingEvents(sub)
	require.Equal(t, []string{EventAuditFlushed + ":audit-1", EventUsageFlushed + ":usage-1"}, eventIDs(t, events))

	var auditData map[string]any
	require.NoError(t, json.Unmarshal(events[0].Data, &auditData))
	assert.Equal(t, EventAuditFlushed, auditData["_live_state"])
	data, ok := auditData["data"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, data, "request_body", "remote bodies stay in the database")
	assert.Equal(t, true, data["request_body_captured"])

	var usageData map[string]any
	require.NoError(t, json.Unmarshal(events[1].Data, &usageData))
	assert.Equal(t, "req-usage-1", usageData["request_id"])
	assert.InDelta(t, 15, usageData["total_tokens"], 0)
}

func TestStartRemoteNeedsAReader(t *testing.T) {
	broker := NewBroker(Config{Enabled: true})
	defer broker.Close()
	broker.StartRemote(RemoteConfig{Writer: testSelf})
	assert.Nil(t, broker.remote)

	disabled := NewBroker(Config{})
	disabled.StartRemote(RemoteConfig{Writer: testSelf, Audit: &fakeAuditTable{}})
	assert.Nil(t, disabled.remote)
}
