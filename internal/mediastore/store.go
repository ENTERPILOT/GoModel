// Package mediastore records the media objects (audio, images, video) the
// gateway keeps outside the database and pairs each record with its bytes in
// a blob store. See docs/adr/0013-media-storage.md.
package mediastore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ErrNotFound reports a media object that does not exist or has expired.
var ErrNotFound = errors.New("media object not found")

// idPattern is the one shape a media id has: "med_" plus a 32-hex UUID.
var idPattern = regexp.MustCompile(`^med_[0-9a-f]{32}$`)

// ValidID reports whether id has the shape the service issues. Callers that
// take ids from clients check it first, so a malformed id never reaches a
// store query or a path.
func ValidID(id string) bool {
	return idPattern.MatchString(id)
}

// Kind classifies a media object by what it holds.
type Kind string

const (
	KindAudio Kind = "audio"
	KindImage Kind = "image"
	KindVideo Kind = "video"
)

// Source records who stored an object, which decides what may read it and
// how it is retained.
type Source string

// SourceAudit marks media captured for the audit log: the payload of an
// audio or image request whose body logging is enabled.
const SourceAudit Source = "audit"

// Object is one stored media payload.
type Object struct {
	ID          string    `json:"id" bson:"_id"`
	Kind        Kind      `json:"kind" bson:"kind"`
	Source      Source    `json:"source" bson:"source"`
	ContentType string    `json:"content_type" bson:"content_type"`
	Bytes       int64     `json:"bytes" bson:"bytes"`
	StorageKey  string    `json:"storage_key" bson:"storage_key"`
	RequestID   string    `json:"request_id,omitempty" bson:"request_id,omitempty"`
	UserPath    string    `json:"user_path,omitempty" bson:"user_path,omitempty"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
	// ExpiresAt is when the retention sweep removes the object; the zero
	// time keeps it forever.
	ExpiresAt time.Time `json:"expires_at,omitzero" bson:"expires_at,omitempty"`
}

// Expired reports whether the object's retention has run out at now.
func (o *Object) Expired(now time.Time) bool {
	return o != nil && !o.ExpiresAt.IsZero() && !o.ExpiresAt.After(now)
}

// Store persists media object records.
type Store interface {
	Insert(ctx context.Context, object *Object) error
	// Get returns the record, or ErrNotFound. Expiry is not applied here;
	// the service checks it so a swept-but-not-yet-deleted row still reads
	// as gone.
	Get(ctx context.Context, id string) (*Object, error)
	// Delete removes the record; a missing record is ErrNotFound.
	Delete(ctx context.Context, id string) error
	// Expired returns up to limit records whose expiry passed before now,
	// oldest expiry first.
	Expired(ctx context.Context, now time.Time, limit int) ([]*Object, error)
	Close() error
}

func normalizeObject(object *Object) (*Object, error) {
	if object == nil {
		return nil, errors.New("media object is nil")
	}
	normalized := *object
	normalized.ID = strings.TrimSpace(normalized.ID)
	normalized.Kind = Kind(strings.TrimSpace(string(normalized.Kind)))
	normalized.Source = Source(strings.TrimSpace(string(normalized.Source)))
	normalized.ContentType = strings.TrimSpace(normalized.ContentType)
	normalized.StorageKey = strings.TrimSpace(normalized.StorageKey)
	normalized.RequestID = strings.TrimSpace(normalized.RequestID)
	normalized.UserPath = strings.TrimSpace(normalized.UserPath)
	normalized.CreatedAt = normalized.CreatedAt.UTC().Truncate(time.Second)
	if !normalized.ExpiresAt.IsZero() {
		normalized.ExpiresAt = normalized.ExpiresAt.UTC().Truncate(time.Second)
	}
	switch {
	case normalized.ID == "":
		return nil, errors.New("media object id is required")
	case normalized.Kind == "":
		return nil, errors.New("media object kind is required")
	case normalized.Source == "":
		return nil, errors.New("media object source is required")
	case normalized.StorageKey == "":
		return nil, errors.New("media object storage key is required")
	case normalized.CreatedAt.IsZero():
		return nil, errors.New("media object created_at is required")
	case normalized.Bytes < 0:
		return nil, fmt.Errorf("media object byte count %d is negative", normalized.Bytes)
	}
	return &normalized, nil
}

// inlineImageTypes are the raster image types a browser renders passively.
// SVG is deliberately absent: it can carry scripts.
var inlineImageTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
	"image/bmp":  true,
	"image/avif": true,
}

// SafeContentType returns the content type an object of kind may be stored
// under. A type is trusted only when it matches the kind (audio/* for audio,
// a passive raster type for images, video/* for video); anything else,
// including a client-declared text/html on an upload, is stored as
// application/octet-stream so it can never be served as active content.
func SafeContentType(kind Kind, contentType string) string {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	switch kind {
	case KindAudio:
		if strings.HasPrefix(contentType, "audio/") {
			return contentType
		}
	case KindImage:
		if inlineImageTypes[contentType] {
			return contentType
		}
	case KindVideo:
		if strings.HasPrefix(contentType, "video/") {
			return contentType
		}
	}
	return "application/octet-stream"
}

// Inline reports whether a stored content type is safe to serve inline. It
// is the same allowlist SafeContentType applies, so an object stored before
// a rule changed is still judged at serve time.
func Inline(contentType string) bool {
	return contentType != "application/octet-stream" && (SafeContentType(KindAudio, contentType) == contentType ||
		SafeContentType(KindImage, contentType) == contentType ||
		SafeContentType(KindVideo, contentType) == contentType)
}
