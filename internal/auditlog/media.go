package auditlog

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/mediastore"
)

// MediaCapturer stores the audio and image payloads of audited requests in
// the media store, so an audit entry carries a media id instead of the bytes
// (docs/adr/0013-media-storage.md). Captured media inherits the audit
// retention. A nil *MediaCapturer stores nothing and every method on it is
// safe to call, which is how "log bodies, but not the media" is expressed.
type MediaCapturer struct {
	store *mediastore.Service
	ttl   time.Duration
}

// NewMediaCapturer binds a media store to the audit retention. retentionDays
// of 0 keeps media forever, matching the audit entries that reference it.
func NewMediaCapturer(store *mediastore.Service, retentionDays int) *MediaCapturer {
	if store == nil {
		return nil
	}
	var ttl time.Duration
	if retentionDays > 0 {
		ttl = time.Duration(retentionDays) * 24 * time.Hour
	}
	return &MediaCapturer{store: store, ttl: ttl}
}

// For binds the capturer to one request's audit entry, or returns nil when
// the request has no live entry (audit logging inactive) so there is nothing
// a stored object could be referenced from.
func (m *MediaCapturer) For(c *echo.Context) *MediaCapture {
	if m == nil {
		return nil
	}
	entry := entryFromContext(c)
	if entry == nil {
		return nil
	}
	return &MediaCapture{
		// Stores outlive the request: a client that disconnects mid-relay
		// must not cancel the commit of what the provider already produced.
		ctx:       context.WithoutCancel(c.Request().Context()),
		store:     m.store,
		ttl:       m.ttl,
		requestID: entry.RequestID,
		userPath:  entry.UserPath,
	}
}

// MediaCapture stores media on behalf of one audit entry. A nil *MediaCapture
// records placeholders only.
type MediaCapture struct {
	ctx       context.Context
	store     *mediastore.Service
	ttl       time.Duration
	requestID string
	userPath  string
}

func (m *MediaCapture) descriptor(kind mediastore.Kind, contentType string) mediastore.Descriptor {
	return mediastore.Descriptor{
		Kind:        kind,
		Source:      mediastore.SourceAudit,
		ContentType: contentType,
		RequestID:   m.requestID,
		UserPath:    m.userPath,
		TTL:         m.ttl,
	}
}

// save stores one payload and returns its record, or nil when nothing was
// stored: capture is off, the payload is empty, or the store failed. A store
// failure is logged; the audit entry then records the payload as not stored.
func (m *MediaCapture) save(kind mediastore.Kind, contentType string, r io.Reader) *mediastore.Object {
	if m == nil || r == nil {
		return nil
	}
	object, err := m.store.Put(m.ctx, m.descriptor(kind, contentType), r)
	if err != nil {
		slog.Warn("audit media capture failed", "kind", kind, "error", err)
		return nil
	}
	if object.Bytes == 0 {
		_ = m.store.Delete(m.ctx, object.ID)
		return nil
	}
	return object
}

// AudioWriter returns a writer that stores the audio bytes written through
// it. On a nil capture it only counts, so a relay can always tee into it and
// build the audit body from the count afterwards.
func (m *MediaCapture) AudioWriter(contentType string) *MediaWriter {
	w := &MediaWriter{contentType: contentType}
	if m == nil {
		return w
	}
	upload, err := m.store.Begin(m.ctx, m.descriptor(mediastore.KindAudio, contentType))
	if err != nil {
		slog.Warn("audit media capture failed", "kind", mediastore.KindAudio, "error", err)
		return w
	}
	w.capture = m
	w.upload = upload
	return w
}

// MediaWriter tees a relayed payload into the media store. Writes never fail
// the relay: after a store error the writer keeps counting and the upload is
// discarded.
type MediaWriter struct {
	capture     *MediaCapture
	upload      *mediastore.Upload
	contentType string
	bytes       int64
	failed      bool
	object      *mediastore.Object
}

// Write counts p and forwards it to the upload while one is open.
func (w *MediaWriter) Write(p []byte) (int, error) {
	w.bytes += int64(len(p))
	if w.upload != nil && !w.failed {
		if _, err := w.upload.Write(p); err != nil {
			w.fail(err)
		}
	}
	return len(p), nil
}

// Bytes reports how many bytes passed through, stored or not.
func (w *MediaWriter) Bytes() int64 {
	return w.bytes
}

func (w *MediaWriter) fail(err error) {
	w.failed = true
	_ = w.upload.Close()
	slog.Warn("audit media capture failed", "kind", mediastore.KindAudio, "error", err)
}

// finish commits the upload once and returns the stored record, or nil when
// nothing was stored.
func (w *MediaWriter) finish() *mediastore.Object {
	if w.upload == nil {
		return w.object
	}
	upload := w.upload
	w.upload = nil
	if w.failed {
		return nil
	}
	if w.bytes == 0 {
		_ = upload.Close()
		return nil
	}
	object, err := upload.Commit(w.capture.ctx)
	if err != nil {
		w.fail(err)
		return nil
	}
	w.object = object
	return object
}
