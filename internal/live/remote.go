package live

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/usage"
)

// The remote feed shows a dashboard the traffic of every replica, not only
// the one its stream is connected to. It adds no write of its own: each
// replica already persists its audit and usage entries in batches, and stamps
// each batch with its writer id and clock (see auditlog.LogEntry.Stamp). While at least one
// dashboard is subscribed, the feed reads the entries other writers stamped in
// the last remoteWindow and publishes the ones it has not published yet. With
// no subscriber it issues no query at all.
//
// Remote entries arrive only once persisted, so a dashboard sees them as
// completed requests (audit.flushed, usage.flushed), up to the peer's flush
// interval plus remoteInterval after they finish, and never their in-flight
// or per-chunk stream events.
const (
	// remoteInterval is how often the feed polls while subscribed.
	remoteInterval = 2 * time.Second

	// remoteWindow is how far back, on this replica's clock, each poll looks
	// for stamped entries. It must exceed remoteInterval plus the time a
	// batch takes to become visible and the clock skew between replicas: an
	// entry stamped earlier than that by the time it is visible is missed.
	// Entries already published are remembered for the window, so the
	// overlap between polls never publishes one twice.
	remoteWindow = 10 * time.Second

	// remoteWriteLimit caps the stamps one poll reads per table, newest
	// first. Busier traffic than that per window loses its oldest entries.
	remoteWriteLimit = 2000

	// remoteEntryLimit caps the entries one poll loads and publishes per
	// table, newest first. The live list shows far fewer; the rest are still
	// in history.
	remoteEntryLimit = 200

	// remotePollTimeout bounds one poll.
	remotePollTimeout = 10 * time.Second
)

// RemoteConfig names the persisted traffic StartRemote publishes.
type RemoteConfig struct {
	// Writer is this process's writer id (storage.WriterID): entries it
	// stamped were already published live and are skipped.
	Writer string
	// Audit and Usage read the stamped entries. Either may be nil.
	Audit auditlog.RecentWriteReader
	Usage usage.RecentWriteReader
}

// StartRemote starts publishing the audit and usage entries other replicas
// persist, polling while at least one subscriber is connected. Close stops
// it. It does nothing on a disabled or closed broker, without a reader, or
// when already started.
func (b *Broker) StartRemote(cfg RemoteConfig) {
	if cfg.Audit == nil && cfg.Usage == nil {
		return
	}
	b.startRemote(newRemoteFeed(b, cfg))
}

func (b *Broker) startRemote(feed *remoteFeed) {
	if b == nil || !b.enabled {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.remote != nil {
		return
	}
	b.remote = feed
	feed.start()
}

// remoteFeed polls the stamped entries of other writers into a broker.
type remoteFeed struct {
	broker   *Broker
	writer   string
	audit    auditlog.RecentWriteReader
	usage    usage.RecentWriteReader
	interval time.Duration
	window   time.Duration

	// now and newTicker are the clock; tests replace them, and set polled
	// to learn when a poll has finished.
	now       func() time.Time
	newTicker func(time.Duration) (ticks <-chan time.Time, stop func())
	polled    func()

	// Owned by the run goroutine.
	auditSeen remoteSeen
	usageSeen remoteSeen

	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func newRemoteFeed(b *Broker, cfg RemoteConfig) *remoteFeed {
	return &remoteFeed{
		broker:    b,
		writer:    cfg.Writer,
		audit:     cfg.Audit,
		usage:     cfg.Usage,
		interval:  remoteInterval,
		window:    remoteWindow,
		now:       time.Now,
		newTicker: realTicker,
		auditSeen: remoteSeen{table: "audit"},
		usageSeen: remoteSeen{table: "usage"},
	}
}

func realTicker(interval time.Duration) (<-chan time.Time, func()) {
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop
}

func (f *remoteFeed) start() {
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.done = make(chan struct{})
	go func() {
		defer close(f.done)
		f.run(ctx)
	}()
}

// stop ends polling and waits for a poll in progress. Safe on nil and more
// than once.
func (f *remoteFeed) stop() {
	if f == nil {
		return
	}
	f.once.Do(func() {
		f.cancel()
		<-f.done
	})
}

// run waits for a subscriber, polls until the last one leaves, and repeats.
func (f *remoteFeed) run(ctx context.Context) {
	for {
		present, _ := f.broker.presence()
		select {
		case <-ctx.Done():
			return
		case <-present:
		}
		f.pollWhileSubscribed(ctx)
		if ctx.Err() != nil {
			return
		}
	}
}

// pollWhileSubscribed polls at once, so a dashboard that just connected
// catches up on the window, then every interval until no subscriber is left.
func (f *remoteFeed) pollWhileSubscribed(ctx context.Context) {
	_, absent := f.broker.presence()
	ticks, stopTicker := f.newTicker(f.interval)
	defer stopTicker()
	f.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-absent:
			return
		case <-ticks:
			f.poll(ctx)
		}
	}
}

func (f *remoteFeed) poll(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, remotePollTimeout)
	defer cancel()
	since := f.now().Add(-f.window).UnixMicro()
	// Audit first: a usage event merges into the audit row it belongs to.
	if f.audit != nil {
		f.auditSeen.poll(ctx, since, f.writer, f.audit.RecentWrites, f.publishAudit)
	}
	if f.usage != nil {
		f.usageSeen.poll(ctx, since, f.writer, f.usage.RecentWrites, f.publishUsage)
	}
	if f.polled != nil {
		f.polled()
	}
}

func (f *remoteFeed) publishAudit(ctx context.Context, ids []string) error {
	entries, err := f.audit.EntriesByID(ctx, ids)
	if err != nil {
		return err
	}
	// Oldest first, so the newest request is the last one a dashboard
	// places, as it would be for local traffic.
	slices.SortFunc(entries, func(a, b auditlog.LogEntry) int {
		return cmp.Or(a.Timestamp.Compare(b.Timestamp), cmp.Compare(a.ID, b.ID))
	})
	for i := range entries {
		f.broker.PublishRemoteAuditEntry(&entries[i])
	}
	return nil
}

func (f *remoteFeed) publishUsage(ctx context.Context, ids []string) error {
	entries, err := f.usage.LogEntriesByID(ctx, ids)
	if err != nil {
		return err
	}
	slices.SortFunc(entries, func(a, b usage.UsageLogEntry) int {
		return cmp.Or(a.Timestamp.Compare(b.Timestamp), cmp.Compare(a.ID, b.ID))
	})
	for _, entry := range entries {
		f.broker.PublishRemoteUsageEntry(entry)
	}
	return nil
}

// remoteSeen remembers which stamped entries of one table were published,
// each until its stamp leaves the window.
type remoteSeen struct {
	table string
	ids   map[string]int64
	// failure is the last logged error, so a lasting one is logged once.
	failure string
}

type recentWritesFunc func(ctx context.Context, since int64, excludeWriter string, limit int) ([]storage.RecentWrite, error)

// poll publishes the entries stamped since that it has not published yet.
// They are remembered only once published, so a failed load is retried on
// the next poll while the entries are still in the window.
func (s *remoteSeen) poll(ctx context.Context, since int64, writer string, recent recentWritesFunc, publish func(context.Context, []string) error) {
	writes, err := recent(ctx, since, writer, remoteWriteLimit)
	if err != nil {
		s.logFailure(ctx, err)
		return
	}
	if s.ids == nil {
		s.ids = make(map[string]int64)
	}
	for id, writtenAt := range s.ids {
		if writtenAt < since {
			delete(s.ids, id)
		}
	}

	// writes is newest first, so a busy window publishes its newest entries.
	var fresh []storage.RecentWrite
	for _, write := range writes {
		if _, ok := s.ids[write.ID]; ok {
			continue
		}
		if len(fresh) == remoteEntryLimit {
			// Over the per-poll budget: skipped for good rather than
			// published late, after newer ones.
			s.ids[write.ID] = write.WrittenAt
			continue
		}
		fresh = append(fresh, write)
	}
	if len(fresh) == 0 {
		s.clearFailure()
		return
	}
	ids := make([]string, len(fresh))
	for i, write := range fresh {
		ids[i] = write.ID
	}
	if err := publish(ctx, ids); err != nil {
		s.logFailure(ctx, err)
		return
	}
	s.clearFailure()
	for _, write := range fresh {
		s.ids[write.ID] = write.WrittenAt
	}
}

func (s *remoteSeen) logFailure(ctx context.Context, err error) {
	// A poll cut short by Close is not a failure; one that timed out is.
	if errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	if message := err.Error(); message != s.failure {
		s.failure = message
		slog.Warn("live logs: failed to read other replicas' "+s.table+" entries; retrying", "error", err)
	}
}

func (s *remoteSeen) clearFailure() {
	s.failure = ""
}
