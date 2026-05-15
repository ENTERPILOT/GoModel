// Package live provides in-process realtime dashboard event fan-out.
package live

import (
	"encoding/json"
	"strings"
	"sync"
	"time"

	"gomodel/internal/auditlog"
	"gomodel/internal/usage"
)

const (
	EventAuditStarted   = "audit.started"
	EventAuditUpdated   = "audit.updated"
	EventAuditCompleted = "audit.completed"
	EventAuditFlushed   = "audit.flushed"
	EventAuditRemoved   = "audit.removed"
	EventUsageCompleted = "usage.completed"
	EventUsageFlushed   = "usage.flushed"
	EventHeartbeat      = "heartbeat"
	EventReset          = "reset"
)

const (
	defaultBufferSize       = 10000
	defaultReplayLimit      = 1000
	defaultSubscriberBuffer = 256
)

// Config controls the in-process live event broker.
type Config struct {
	Enabled          bool
	BufferSize       int
	ReplayLimit      int
	SubscriberBuffer int
	Heartbeat        time.Duration
}

// Event is the stable envelope sent over the live dashboard stream.
type Event struct {
	Seq       uint64          `json:"seq"`
	Type      string          `json:"type"`
	RequestID string          `json:"request_id,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// Subscription is one live stream consumer.
type Subscription struct {
	Replay []Event
	Events <-chan Event
	Reset  bool

	broker *Broker
	id     uint64
}

// Close unregisters the subscription.
func (s *Subscription) Close() {
	if s == nil || s.broker == nil {
		return
	}
	s.broker.unsubscribe(s.id)
}

// Broker stores a bounded replay window and fans live events out to subscribers.
type Broker struct {
	enabled          bool
	bufferSize       int
	replayLimit      int
	subscriberBuffer int
	heartbeat        time.Duration

	mu          sync.Mutex
	nextSeq     uint64
	nextSubID   uint64
	closed      bool
	events      []Event
	subscribers map[uint64]chan Event
}

// NewBroker creates a live event broker. A disabled broker is safe to use.
func NewBroker(cfg Config) *Broker {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = defaultBufferSize
	}
	if cfg.ReplayLimit <= 0 {
		cfg.ReplayLimit = defaultReplayLimit
	}
	if cfg.SubscriberBuffer <= 0 {
		cfg.SubscriberBuffer = defaultSubscriberBuffer
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = 15 * time.Second
	}
	return &Broker{
		enabled:          cfg.Enabled,
		bufferSize:       cfg.BufferSize,
		replayLimit:      cfg.ReplayLimit,
		subscriberBuffer: cfg.SubscriberBuffer,
		heartbeat:        cfg.Heartbeat,
		subscribers:      make(map[uint64]chan Event),
	}
}

// Enabled reports whether this broker should accept dashboard subscriptions.
func (b *Broker) Enabled() bool {
	if b == nil || !b.enabled {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.closed
}

// Heartbeat returns the stream heartbeat interval.
func (b *Broker) Heartbeat() time.Duration {
	if b == nil || b.heartbeat <= 0 {
		return 15 * time.Second
	}
	return b.heartbeat
}

// LatestSeq returns the newest assigned stream sequence.
func (b *Broker) LatestSeq() uint64 {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nextSeq
}

// Subscribe registers a client and returns replay events after cursor.
func (b *Broker) Subscribe(cursor uint64) *Subscription {
	if b == nil || !b.enabled {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}

	replay, reset := b.replayAfterLocked(cursor)
	b.nextSubID++
	id := b.nextSubID
	ch := make(chan Event, b.subscriberBuffer)
	b.subscribers[id] = ch

	return &Subscription{
		Replay: replay,
		Events: ch,
		Reset:  reset,
		broker: b,
		id:     id,
	}
}

func (b *Broker) replayAfterLocked(cursor uint64) ([]Event, bool) {
	if cursor == 0 || len(b.events) == 0 {
		return nil, false
	}
	oldest := b.events[0].Seq
	if cursor < oldest-1 {
		return nil, true
	}
	replay := make([]Event, 0, min(len(b.events), b.replayLimit))
	for _, event := range b.events {
		if event.Seq > cursor {
			replay = append(replay, event)
		}
	}
	if len(replay) > b.replayLimit {
		replay = replay[len(replay)-b.replayLimit:]
	}
	return replay, false
}

func (b *Broker) unsubscribe(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch, ok := b.subscribers[id]
	if !ok {
		return
	}
	delete(b.subscribers, id)
	close(ch)
}

// Close terminates all active subscribers and prevents future live events.
func (b *Broker) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subscribers := b.subscribers
	b.subscribers = make(map[uint64]chan Event)
	b.mu.Unlock()

	for _, ch := range subscribers {
		close(ch)
	}
}

func (b *Broker) publish(eventType, requestID string, timestamp time.Time, payload any) {
	if b == nil || !b.enabled {
		return
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		return
	}
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}

	b.nextSeq++
	event := Event{
		Seq:       b.nextSeq,
		Type:      eventType,
		RequestID: strings.TrimSpace(requestID),
		Timestamp: timestamp.UTC(),
		Data:      data,
	}
	b.events = append(b.events, event)
	if len(b.events) > b.bufferSize {
		drop := len(b.events) - b.bufferSize
		b.events = append([]Event(nil), b.events[drop:]...)
	}

	for id, ch := range b.subscribers {
		select {
		case ch <- event:
		default:
			delete(b.subscribers, id)
			close(ch)
		}
	}
}

// PublishAuditEvent publishes a compact audit log preview event.
func (b *Broker) PublishAuditEvent(eventType string, entry *auditlog.LogEntry) {
	if entry == nil {
		return
	}
	payload := auditPreviewFromEntry(eventType, entry)
	b.publish(eventType, entry.RequestID, entry.Timestamp, payload)
}

// PublishUsageEvent publishes a compact usage log event.
func (b *Broker) PublishUsageEvent(eventType string, entry *usage.UsageEntry) {
	if entry == nil {
		return
	}
	payload := usagePreviewFromEntry(entry)
	b.publish(eventType, entry.RequestID, entry.Timestamp, payload)
}

type auditPreview struct {
	ID                string    `json:"id"`
	RequestID         string    `json:"request_id,omitempty"`
	Timestamp         time.Time `json:"timestamp"`
	DurationNs        *int64    `json:"duration_ns,omitempty"`
	RequestedModel    string    `json:"requested_model,omitempty"`
	ResolvedModel     string    `json:"resolved_model,omitempty"`
	Provider          string    `json:"provider,omitempty"`
	ProviderName      string    `json:"provider_name,omitempty"`
	AliasUsed         bool      `json:"alias_used,omitempty"`
	WorkflowVersionID string    `json:"workflow_version_id,omitempty"`
	CacheType         string    `json:"cache_type,omitempty"`
	StatusCode        *int      `json:"status_code,omitempty"`
	AuthKeyID         string    `json:"auth_key_id,omitempty"`
	AuthMethod        string    `json:"auth_method,omitempty"`
	ClientIP          string    `json:"client_ip,omitempty"`
	Method            string    `json:"method,omitempty"`
	Path              string    `json:"path,omitempty"`
	UserPath          string    `json:"user_path,omitempty"`
	Stream            bool      `json:"stream,omitempty"`
	ErrorType         string    `json:"error_type,omitempty"`
	ErrorMessage      string    `json:"error_message,omitempty"`
	LiveState         string    `json:"_live_state,omitempty"`
	LivePending       bool      `json:"_live_pending,omitempty"`
}

func auditPreviewFromEntry(eventType string, entry *auditlog.LogEntry) auditPreview {
	preview := auditPreview{
		ID:                entry.ID,
		RequestID:         entry.RequestID,
		Timestamp:         entry.Timestamp.UTC(),
		RequestedModel:    entry.RequestedModel,
		ResolvedModel:     entry.ResolvedModel,
		Provider:          entry.Provider,
		ProviderName:      entry.ProviderName,
		AliasUsed:         entry.AliasUsed,
		WorkflowVersionID: entry.WorkflowVersionID,
		CacheType:         entry.CacheType,
		AuthKeyID:         entry.AuthKeyID,
		AuthMethod:        entry.AuthMethod,
		ClientIP:          entry.ClientIP,
		Method:            entry.Method,
		Path:              entry.Path,
		UserPath:          entry.UserPath,
		Stream:            entry.Stream,
		ErrorType:         entry.ErrorType,
		LiveState:         eventType,
		LivePending:       eventType != EventAuditFlushed,
	}
	if entry.DurationNs > 0 {
		duration := entry.DurationNs
		preview.DurationNs = &duration
	}
	if entry.StatusCode > 0 {
		status := entry.StatusCode
		preview.StatusCode = &status
	}
	if entry.Data != nil {
		preview.ErrorMessage = entry.Data.ErrorMessage
	}
	return preview
}

func usagePreviewFromEntry(entry *usage.UsageEntry) usage.UsageLogEntry {
	return usage.UsageLogEntry{
		ID:                     entry.ID,
		RequestID:              entry.RequestID,
		ProviderID:             entry.ProviderID,
		Timestamp:              entry.Timestamp.UTC(),
		Model:                  entry.Model,
		Provider:               entry.Provider,
		ProviderName:           entry.ProviderName,
		Endpoint:               entry.Endpoint,
		UserPath:               entry.UserPath,
		CacheType:              entry.CacheType,
		InputTokens:            entry.InputTokens,
		OutputTokens:           entry.OutputTokens,
		TotalTokens:            entry.TotalTokens,
		InputCost:              entry.InputCost,
		OutputCost:             entry.OutputCost,
		TotalCost:              entry.TotalCost,
		CostSource:             entry.CostSource,
		CostsCalculationCaveat: entry.CostsCalculationCaveat,
	}
}
