package live

import (
	"math"
	"time"

	"github.com/goccy/go-json"
)

// Budgets for retainedSizeBound. Each is well above what the members it covers
// encode to, and above what they occupy in memory.
const (
	// previewFieldsBound covers the keys, punctuation and non-text values of
	// one auditPreview or one auditPreviewData.
	previewFieldsBound = 2048
	// snapshotFieldsBound does the same for one attempt or guardrail outcome.
	snapshotFieldsBound = 1024
	// headerEntryBound covers one header's quotes and punctuation, and its
	// map overhead in memory.
	headerEntryBound = 64
	// escapedByteBound is the most one byte of text encodes to (\u00XX).
	escapedByteBound = 6
)

// retainedSizeBound returns an upper bound on the encoded size of a retained
// preview, or math.MaxInt when it holds anything it cannot bound or that might
// not encode. A preview within maxRetainedEventBytes is kept unencoded: its
// encoding cannot exceed the cap, so encoding it at publish would never have
// compacted it, and it holds no more memory than its encoding would.
func retainedSizeBound(p *auditPreview) int {
	if !encodableTime(p.Timestamp) {
		return math.MaxInt
	}
	size := previewFieldsBound + textBound(p.ID, p.RequestID, p.RequestedModel, p.ResolvedModel,
		p.Provider, p.ProviderName, p.WorkflowVersionID, p.CacheType, p.AuthKeyID, p.AuthMethod,
		p.ClientIP, p.Method, p.Path, p.UserPath, p.SessionID, p.ErrorType, p.ErrorMessage, p.LiveState)
	d := p.Data
	if d == nil {
		return size
	}
	if d.RequestBody != nil || d.ResponseBody != nil ||
		(d.Temperature != nil && (math.IsNaN(*d.Temperature) || math.IsInf(*d.Temperature, 0))) {
		return math.MaxInt
	}
	size += previewFieldsBound + textBound(d.UserAgent, d.APIKeyHash, d.ErrorMessage, d.ErrorCode)
	if d.Failover != nil {
		size += textBound(d.Failover.TargetModel)
	}
	size += headersBound(d.RequestHeaders) + headersBound(d.ResponseHeaders)
	for i := range d.Attempts {
		a := &d.Attempts[i]
		if a.ResponseBody != nil || len(a.ResponseHeaders) > 0 || !encodableTime(a.StartedAt) {
			return math.MaxInt
		}
		size += snapshotFieldsBound + textBound(a.Kind, a.ProviderType, a.ProviderName, a.Model,
			a.ErrorType, a.ErrorCode, a.ErrorMessage)
	}
	for i := range d.Guardrails {
		g := &d.Guardrails[i]
		if g.Detail != nil {
			return math.MaxInt
		}
		size += snapshotFieldsBound + textBound(g.Phase, g.Instance, g.Type, g.Action, g.Code,
			g.Message, g.Error, g.FailMode, g.Target)
	}
	return size
}

func textBound(texts ...string) int {
	size := 0
	for _, text := range texts {
		size += escapedByteBound * len(text)
	}
	return size
}

func headersBound(headers map[string]string) int {
	size := 0
	for key, value := range headers {
		size += headerEntryBound + textBound(key, value)
	}
	return size
}

// encodableTime reports whether t has a JSON encoding: MarshalJSON rejects
// years outside [0, 9999].
func encodableTime(t time.Time) bool {
	year := t.Year()
	return year >= 0 && year <= 9999
}

// retainedEvent is an event as the replay ring and active snapshots keep it.
// An audit event nobody received at publish keeps its preview and leaves Data
// nil: the broker encodes it only for a subscriber that replays it, so with no
// dashboard connected it never encodes at all. The preview is shared with the
// request's active snapshot and is never mutated once stored.
type retainedEvent struct {
	Event
	preview *auditPreview
}

// encodeRetainedEvents returns the events as subscribers receive them,
// encoding those that still carry their preview. One that fails to encode is
// dropped, as publishing it would have been; retainedSizeBound keeps such
// previews from being stored unencoded.
func encodeRetainedEvents(retained []retainedEvent) []Event {
	events := make([]Event, 0, len(retained))
	for _, event := range retained {
		if event.preview != nil {
			data, err := json.Marshal(event.preview)
			if err != nil {
				continue
			}
			event.Data = data
		}
		events = append(events, event.Event)
	}
	return events
}
