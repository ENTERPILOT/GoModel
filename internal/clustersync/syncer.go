// Package clustersync propagates admin configuration changes between gateway
// instances that share one database.
//
// Admin-managed configuration is durable in the database but served from
// in-memory snapshots on each instance. Every successful admin write bumps a
// per-entity revision in the config_revisions table; every instance polls that
// tiny table and refreshes the snapshot of each entity whose revision moved.
// It needs nothing but the shared database, so it works the same on SQLite,
// PostgreSQL, and MongoDB.
package clustersync

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

// syncInterval is how often the revisions are polled: the upper bound, plus
// the refresh itself, on how long a change takes to reach another instance.
const syncInterval = 2 * time.Second

// bumpTimeout bounds announcing one change. The announcement outlives the
// admin request that triggered it, so a client that disconnects right after a
// save still has it reach the other instances.
const bumpTimeout = 5 * time.Second

// Syncer polls the revision table and refreshes changed entities.
type Syncer struct {
	store    Store
	interval time.Duration

	mu        sync.Mutex
	order     []string
	refreshes map[string]func(context.Context) error
	seen      map[string]int64
	// unannounced holds the entities whose announcement failed, each with
	// the sequence number of its latest failure; every tick retries them
	// until one succeeds.
	unannounced map[string]uint64
	failureSeq  uint64
	// failures holds the last logged error per key (see logFailure), so
	// a persistent failure is logged once, not every tick.
	failures map[string]string
	cancel   context.CancelFunc
	done     chan struct{}
	closed   bool
}

// New opens the revision store on the shared storage connection. Call it
// before the services it refreshes load their snapshots: see NewSyncer.
func New(ctx context.Context, shared storage.Storage) (*Syncer, error) {
	if shared == nil {
		return nil, fmt.Errorf("shared storage is required")
	}
	store, err := storage.ResolveSQLBackend[Store](ctx, shared,
		func(db sqlx.DB) (Store, error) { return NewSQLStore(ctx, db) },
		func(db *mongo.Database) (Store, error) { return NewMongoDBStore(db) },
	)
	if err != nil {
		return nil, err
	}
	return NewSyncer(ctx, store)
}

// NewSyncer records the current revisions as already applied. Taking them
// before the services load means a change made while they load is refreshed
// on the first tick, and nothing is refreshed needlessly at startup.
func NewSyncer(ctx context.Context, store Store) (*Syncer, error) {
	if store == nil {
		return nil, fmt.Errorf("config revision store is required")
	}
	seen, err := store.Revisions(ctx)
	if err != nil {
		return nil, err
	}
	if seen == nil {
		seen = make(map[string]int64)
	}
	return &Syncer{
		store:       store,
		interval:    syncInterval,
		refreshes:   make(map[string]func(context.Context) error),
		seen:        seen,
		unannounced: make(map[string]uint64),
		failures:    make(map[string]string),
	}, nil
}

// Register sets how entity's snapshot is reloaded when another instance (or
// this one) changes it. Entities refresh in registration order. Register
// every entity before Start.
func (s *Syncer) Register(entity string, refresh func(context.Context) error) {
	if s == nil || refresh == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.refreshes[entity]; !ok {
		s.order = append(s.order, entity)
	}
	s.refreshes[entity] = refresh
}

// Entities lists the registered entities in refresh order.
func (s *Syncer) Entities() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.order)
}

// Notifier returns the function a service calls after each successful admin
// write of entity. A failed announcement is logged and retried on every tick
// until one succeeds, not returned: the write itself succeeded and this
// instance already serves it.
func (s *Syncer) Notifier(entity string) func(context.Context) {
	if s == nil {
		return nil
	}
	return func(ctx context.Context) {
		s.announce(context.WithoutCancel(ctx), entity)
	}
}

// announce bumps entity's revision, or keeps it for the next tick to retry.
// Bumps only need to move the revision, so retries of one entity coalesce.
func (s *Syncer) announce(ctx context.Context, entity string) {
	ctx, cancel := context.WithTimeout(ctx, bumpTimeout)
	defer cancel()
	s.mu.Lock()
	pending := s.unannounced[entity]
	s.mu.Unlock()
	err := s.store.Bump(ctx, entity)
	s.mu.Lock()
	switch {
	case err != nil:
		s.failureSeq++
		s.unannounced[entity] = s.failureSeq
	case s.unannounced[entity] == pending:
		// No announcement failed while this one ran, so every change made
		// before it is announced.
		delete(s.unannounced, entity)
	}
	s.mu.Unlock()
	key := "announce:" + entity
	if err != nil {
		s.logFailure(key, "configuration change saved but not yet announced to other gateway instances; retrying", err, "entity", entity)
		return
	}
	s.clearFailure(key)
}

// Start polls until Close or until ctx ends. It is idempotent.
func (s *Syncer) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.cancel != nil {
		return
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.done = make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.poll(ctx)
			}
		}
	}()
}

// Close stops polling and waits for a tick in progress to finish.
func (s *Syncer) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.closed = true
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	return nil
}

// poll retries the failed announcements, then reads every revision and
// refreshes each registered entity whose revision moved. An entity is marked
// applied only after its refresh succeeds, so a failed one is retried on the
// next tick.
func (s *Syncer) poll(ctx context.Context) {
	for _, entity := range s.unannouncedEntities() {
		if ctx.Err() != nil {
			return
		}
		s.announce(ctx, entity)
	}
	revisions, err := s.store.Revisions(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logFailure("", "failed to read configuration revisions", err)
		}
		return
	}
	s.clearFailure("")
	for _, entity := range s.changed(revisions) {
		s.mu.Lock()
		refresh := s.refreshes[entity]
		s.mu.Unlock()
		if err := refresh(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			s.logFailure(entity, "failed to apply a configuration change made on another gateway instance", err, "entity", entity)
			continue
		}
		s.clearFailure(entity)
		s.mu.Lock()
		s.seen[entity] = revisions[entity]
		s.mu.Unlock()
	}
}

// changed lists, in registration order, the registered entities whose stored
// revision differs from the last one applied.
func (s *Syncer) changed(revisions map[string]int64) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var entities []string
	for _, entity := range s.order {
		if revision, ok := revisions[entity]; ok && revision != s.seen[entity] {
			entities = append(entities, entity)
		}
	}
	return entities
}

// unannouncedEntities lists, sorted, the entities whose announcement failed.
func (s *Syncer) unannouncedEntities() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Sorted(maps.Keys(s.unannounced))
}

// logFailure logs err unless it is the error last logged under key: "" for
// reading the revisions, the entity for refreshing it, and "announce:" plus
// the entity for announcing it.
func (s *Syncer) logFailure(key, message string, err error, attrs ...any) {
	s.mu.Lock()
	repeated := s.failures[key] == err.Error()
	s.failures[key] = err.Error()
	s.mu.Unlock()
	if repeated {
		return
	}
	slog.Warn(message, append(attrs, "error", err)...)
}

func (s *Syncer) clearFailure(key string) {
	s.mu.Lock()
	delete(s.failures, key)
	s.mu.Unlock()
}
