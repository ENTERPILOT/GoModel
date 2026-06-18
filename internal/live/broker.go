// Package live provides in-process realtime dashboard event fan-out.
package live

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

	"gomodel/internal/auditlog"
	"gomodel/internal/usage"
)

const (
	EventAuditStarted   = auditlog.LiveEventAuditStarted
	EventAuditUpdated   = auditlog.LiveEventAuditUpdated
	EventAuditCompleted = auditlog.LiveEventAuditCompleted
	EventAuditFailed    = auditlog.LiveEventAuditFailed
	EventAuditFlushed   = auditlog.LiveEventAuditFlushed
	EventAuditRemoved   = auditlog.LiveEventAuditRemoved
	EventUsageCompleted = usage.LiveEventUsageCompleted
	EventUsageFailed    = usage.LiveEventUsageFailed
	EventUsageFlushed   = usage.LiveEventUsageFlushed
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
	activeAudit map[string]Event
	activeUsage map[string]Event
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
		activeAudit:      make(map[string]Event),
		activeUsage:      make(map[string]Event),
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
	if cursor == 0 {
		return b.activeSnapshotsLocked(), false
	}
	if len(b.events) == 0 {
		return b.activeSnapshotsLocked(), true
	}
	latest := b.events[len(b.events)-1].Seq
	if cursor > latest {
		return b.activeSnapshotsLocked(), true
	}
	oldest := b.events[0].Seq
	if cursor < oldest-1 {
		return b.activeSnapshotsLocked(), true
	}
	if cursor < latest && latest-cursor > uint64(b.replayLimit) {
		return b.activeSnapshotsLocked(), true
	}
	replay := make([]Event, 0, min(len(b.events), b.replayLimit))
	for _, event := range b.events {
		if event.Seq > cursor {
			replay = append(replay, event)
		}
	}
	return replay, false
}

func (b *Broker) activeSnapshotsLocked() []Event {
	snapshots := make([]Event, 0, len(b.activeAudit)+len(b.activeUsage))
	for _, event := range b.activeAudit {
		snapshots = append(snapshots, event)
	}
	for _, event := range b.activeUsage {
		snapshots = append(snapshots, event)
	}
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Seq < snapshots[j].Seq
	})
	return snapshots
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
	b.updateActiveSnapshotsLocked(&event)
	b.events = append(b.events, event)
	if len(b.events) > b.bufferSize {
		drop := len(b.events) - b.bufferSize
		copy(b.events, b.events[drop:])
		for i := b.bufferSize; i < len(b.events); i++ {
			b.events[i] = Event{}
		}
		b.events = b.events[:b.bufferSize]
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

func (b *Broker) updateActiveSnapshotsLocked(event *Event) {
	if event == nil {
		return
	}
	switch event.Type {
	case EventAuditFailed, EventAuditFlushed, EventAuditRemoved:
		deleteActiveSnapshot(b.activeAudit, auditActiveKeys(*event))
		return
	case EventUsageFailed, EventUsageFlushed:
		deleteActiveSnapshot(b.activeUsage, usageActiveKeys(*event))
		return
	}

	if strings.HasPrefix(event.Type, "audit.") {
		keys := auditActiveKeys(*event)
		if keys.canonical == "" {
			return
		}
		if previous, ok := findActiveSnapshot(b.activeAudit, keys); ok {
			event.Data = mergeEventData(previous.Data, event.Data)
		}
		b.activeAudit[keys.canonical] = *event
		deleteActiveSnapshotAliases(b.activeAudit, keys)
		return
	}
	if strings.HasPrefix(event.Type, "usage.") {
		keys := usageActiveKeys(*event)
		if keys.canonical == "" {
			return
		}
		if previous, ok := findActiveSnapshot(b.activeUsage, keys); ok {
			event.Data = mergeEventData(previous.Data, event.Data)
		}
		b.activeUsage[keys.canonical] = *event
		deleteActiveSnapshotAliases(b.activeUsage, keys)
	}
}

type eventIdentity struct {
	ID        string `json:"id"`
	RequestID string `json:"request_id"`
}

type activeSnapshotKeys struct {
	canonical string
	aliases   []string
}

func auditActiveKeys(event Event) activeSnapshotKeys {
	identity := eventIdentityFromData(event.Data)
	requestID := strings.TrimSpace(event.RequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(identity.RequestID)
	}
	id := strings.TrimSpace(identity.ID)
	keys := activeSnapshotKeys{}
	if requestID != "" {
		keys.canonical = "request:" + requestID
		if id != "" {
			keys.aliases = append(keys.aliases, "id:"+id)
		}
		return keys
	}
	if id != "" {
		keys.canonical = "id:" + id
	}
	return keys
}

func usageActiveKeys(event Event) activeSnapshotKeys {
	identity := eventIdentityFromData(event.Data)
	id := strings.TrimSpace(identity.ID)
	requestID := strings.TrimSpace(event.RequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(identity.RequestID)
	}
	keys := activeSnapshotKeys{}
	if id != "" {
		keys.canonical = "id:" + id
		if requestID != "" {
			keys.aliases = append(keys.aliases, "request:"+requestID)
		}
		return keys
	}
	if requestID != "" {
		keys.canonical = "request:" + requestID
	}
	return keys
}

func findActiveSnapshot(snapshots map[string]Event, keys activeSnapshotKeys) (Event, bool) {
	if event, ok := snapshots[keys.canonical]; ok {
		return event, true
	}
	for _, key := range keys.aliases {
		if event, ok := snapshots[key]; ok {
			return event, true
		}
	}
	return Event{}, false
}

func deleteActiveSnapshot(snapshots map[string]Event, keys activeSnapshotKeys) {
	if keys.canonical != "" {
		delete(snapshots, keys.canonical)
	}
	for _, key := range keys.aliases {
		delete(snapshots, key)
	}
}

func deleteActiveSnapshotAliases(snapshots map[string]Event, keys activeSnapshotKeys) {
	for _, key := range keys.aliases {
		delete(snapshots, key)
	}
}

func eventIdentityFromData(data json.RawMessage) eventIdentity {
	var identity eventIdentity
	_ = json.Unmarshal(data, &identity)
	return identity
}

func mergeEventData(base, patch json.RawMessage) json.RawMessage {
	var baseObject map[string]json.RawMessage
	var patchObject map[string]json.RawMessage
	if err := json.Unmarshal(base, &baseObject); err != nil || baseObject == nil {
		return append(json.RawMessage(nil), patch...)
	}
	if err := json.Unmarshal(patch, &patchObject); err != nil || patchObject == nil {
		return append(json.RawMessage(nil), patch...)
	}
	for key, value := range patchObject {
		baseObject[key] = mergeEventDataValue(baseObject[key], value)
	}
	merged, err := json.Marshal(baseObject)
	if err != nil {
		return append(json.RawMessage(nil), patch...)
	}
	return merged
}

func mergeEventDataValue(base, patch json.RawMessage) json.RawMessage {
	if len(base) == 0 || len(patch) == 0 {
		return append(json.RawMessage(nil), patch...)
	}
	var baseObject map[string]json.RawMessage
	var patchObject map[string]json.RawMessage
	if err := json.Unmarshal(base, &baseObject); err != nil || baseObject == nil {
		return append(json.RawMessage(nil), patch...)
	}
	if err := json.Unmarshal(patch, &patchObject); err != nil || patchObject == nil {
		return append(json.RawMessage(nil), patch...)
	}
	for key, value := range patchObject {
		baseObject[key] = mergeEventDataValue(baseObject[key], value)
	}
	merged, err := json.Marshal(baseObject)
	if err != nil {
		return append(json.RawMessage(nil), patch...)
	}
	return merged
}

// PublishAuditEvent publishes a compact audit log preview event.
func (b *Broker) PublishAuditEvent(eventType string, entry *auditlog.LogEntry) {
	if entry == nil {
		return
	}
	payload := auditPreviewFromEntry(eventType, entry)
	b.publish(eventType, entry.RequestID, entry.Timestamp, payload)
}

// PublishUsageEvent publishes a compact usage log event. Cached usage entries
// are broadcast like any other so the dashboard can choose to surface or hide
// them via the "Hide cached requests" toggle on the Usage page.
func (b *Broker) PublishUsageEvent(eventType string, entry *usage.UsageEntry) {
	if entry == nil {
		return
	}
	payload := usagePreviewFromEntry(entry)
	b.publish(eventType, entry.RequestID, entry.Timestamp, payload)
}

type auditPreview struct {
	ID                string            `json:"id"`
	RequestID         string            `json:"request_id,omitempty"`
	Timestamp         time.Time         `json:"timestamp"`
	DurationNs        *int64            `json:"duration_ns,omitempty"`
	RequestedModel    string            `json:"requested_model,omitempty"`
	ResolvedModel     string            `json:"resolved_model,omitempty"`
	Provider          string            `json:"provider,omitempty"`
	ProviderName      string            `json:"provider_name,omitempty"`
	AliasUsed         bool              `json:"alias_used,omitempty"`
	WorkflowVersionID string            `json:"workflow_version_id,omitempty"`
	CacheType         string            `json:"cache_type,omitempty"`
	StatusCode        *int              `json:"status_code,omitempty"`
	AuthKeyID         string            `json:"auth_key_id,omitempty"`
	AuthMethod        string            `json:"auth_method,omitempty"`
	ClientIP          string            `json:"client_ip,omitempty"`
	Method            string            `json:"method,omitempty"`
	Path              string            `json:"path,omitempty"`
	UserPath          string            `json:"user_path,omitempty"`
	Stream            bool              `json:"stream,omitempty"`
	ErrorType         string            `json:"error_type,omitempty"`
	ErrorMessage      string            `json:"error_message,omitempty"`
	Data              *auditPreviewData `json:"data,omitempty"`
	LiveState         string            `json:"_live_state,omitempty"`
	LivePending       bool              `json:"_live_pending,omitempty"`
}

type auditPreviewData struct {
	UserAgent                  string                             `json:"user_agent,omitempty"`
	APIKeyHash                 string                             `json:"api_key_hash,omitempty"`
	WorkflowFeatures           *auditlog.WorkflowFeaturesSnapshot `json:"workflow_features,omitempty"`
	Failover                   *auditlog.FailoverSnapshot         `json:"failover,omitempty"`
	Temperature                *float64                           `json:"temperature,omitempty"`
	MaxTokens                  *int                               `json:"max_tokens,omitempty"`
	ErrorMessage               string                             `json:"error_message,omitempty"`
	ErrorCode                  string                             `json:"error_code,omitempty"`
	RequestHeaders             map[string]string                  `json:"request_headers,omitempty"`
	ResponseHeaders            map[string]string                  `json:"response_headers,omitempty"`
	RequestBody                any                                `json:"request_body,omitempty"`
	ResponseBody               any                                `json:"response_body,omitempty"`
	RequestBodyTooBigToHandle  bool                               `json:"request_body_too_big_to_handle,omitempty"`
	ResponseBodyTooBigToHandle bool                               `json:"response_body_too_big_to_handle,omitempty"`
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
		LivePending:       !auditEventTerminal(eventType),
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
		data := auditPreviewData{
			WorkflowFeatures: entry.Data.WorkflowFeatures,
			Failover:         entry.Data.Failover,
		}
		if auditPreviewIncludesLiveRequestMetadata(eventType) {
			data.UserAgent = entry.Data.UserAgent
			data.APIKeyHash = entry.Data.APIKeyHash
			data.RequestHeaders = entry.Data.RequestHeaders
		}
		if auditPreviewIncludesLiveRequestBody(eventType) {
			data.RequestBody = entry.Data.RequestBody
			data.RequestBodyTooBigToHandle = entry.Data.RequestBodyTooBigToHandle
		}
		if auditPreviewIncludesCapturedData(eventType) {
			data.UserAgent = entry.Data.UserAgent
			data.APIKeyHash = entry.Data.APIKeyHash
			data.Temperature = entry.Data.Temperature
			data.MaxTokens = entry.Data.MaxTokens
			data.ErrorMessage = entry.Data.ErrorMessage
			data.ErrorCode = entry.Data.ErrorCode
			data.RequestHeaders = entry.Data.RequestHeaders
			data.ResponseHeaders = entry.Data.ResponseHeaders
			data.RequestBody = entry.Data.RequestBody
			data.ResponseBody = entry.Data.ResponseBody
			data.RequestBodyTooBigToHandle = entry.Data.RequestBodyTooBigToHandle
			data.ResponseBodyTooBigToHandle = entry.Data.ResponseBodyTooBigToHandle
		}
		if data.hasValues() {
			preview.Data = &data
		}
	}
	return preview
}

func auditPreviewIncludesLiveRequestMetadata(eventType string) bool {
	return eventType == EventAuditStarted || eventType == EventAuditUpdated
}

func auditPreviewIncludesLiveRequestBody(eventType string) bool {
	return eventType == EventAuditUpdated
}

func auditPreviewIncludesCapturedData(eventType string) bool {
	return eventType == EventAuditCompleted || eventType == EventAuditFlushed || eventType == EventAuditFailed
}

func (d auditPreviewData) hasValues() bool {
	return d.UserAgent != "" ||
		d.APIKeyHash != "" ||
		d.WorkflowFeatures != nil ||
		d.Failover != nil ||
		d.Temperature != nil ||
		d.MaxTokens != nil ||
		d.ErrorMessage != "" ||
		d.ErrorCode != "" ||
		len(d.RequestHeaders) > 0 ||
		len(d.ResponseHeaders) > 0 ||
		d.RequestBody != nil ||
		d.ResponseBody != nil ||
		d.RequestBodyTooBigToHandle ||
		d.ResponseBodyTooBigToHandle
}

func auditEventTerminal(eventType string) bool {
	return eventType == EventAuditFailed || eventType == EventAuditFlushed || eventType == EventAuditRemoved
}

func usagePreviewFromEntry(entry *usage.UsageEntry) usage.UsageLogEntry {
	preview := usage.UsageLogEntry{
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
		RawData:                copyRawData(entry.RawData),
		CostsCalculationCaveat: entry.CostsCalculationCaveat,
	}
	usage.EnrichUsageLogEntry(&preview)
	return preview
}

func copyRawData(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = copyRawValue(value)
	}
	return dst
}

func copyRawValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		dst := make(map[string]any, len(typed))
		for key, value := range typed {
			dst[key] = copyRawValue(value)
		}
		return dst
	case []any:
		dst := make([]any, len(typed))
		for i, value := range typed {
			dst[i] = copyRawValue(value)
		}
		return dst
	default:
		return value
	}
}
