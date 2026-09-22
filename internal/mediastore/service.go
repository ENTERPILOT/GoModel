package mediastore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/blobstore"
	"github.com/enterpilot/gomodel/internal/storage"
)

const (
	// SweepInterval is how often expired objects are removed.
	SweepInterval = time.Hour
	// sweepBatch bounds one round of the retention sweep so a large backlog
	// is worked through in bounded queries.
	sweepBatch = 500
)

// Descriptor says what an object holds and who it belongs to.
type Descriptor struct {
	Kind        Kind
	Source      Source
	ContentType string
	RequestID   string
	UserPath    string
	// TTL is how long the object is kept; zero keeps it forever.
	TTL time.Duration
}

// Service stores media: a record per object in a Store and the bytes in a
// blobstore.Store. It runs the retention sweep until closed.
type Service struct {
	objects   Store
	blobs     blobstore.Store
	now       func() time.Time
	stop      chan struct{}
	closeOnce sync.Once
}

// NewService pairs a record store with a blob store and starts the hourly
// retention sweep.
func NewService(objects Store, blobs blobstore.Store) *Service {
	return newService(objects, blobs, time.Now, true)
}

// newService is the constructor tests use: an injectable clock, and the
// choice not to start the sweep goroutine so the clock can be moved without
// racing it. Sweep is then driven by calling it directly.
func newService(objects Store, blobs blobstore.Store, now func() time.Time, runSweep bool) *Service {
	s := &Service{
		objects: objects,
		blobs:   blobs,
		now:     now,
		stop:    make(chan struct{}),
	}
	if runSweep {
		go storage.RunCleanupLoop(s.stop, SweepInterval, s.sweep)
	}
	return s
}

// Upload is an object being written. Bytes go straight to the blob store;
// the record is written on Commit, so a discarded upload leaves nothing.
type Upload struct {
	service *Service
	writer  blobstore.Writer
	object  Object
	done    bool
}

// Begin opens an upload for an object described by d.
func (s *Service) Begin(ctx context.Context, d Descriptor) (*Upload, error) {
	now := s.now().UTC()
	object := Object{
		ID:          newObjectID(),
		Kind:        d.Kind,
		Source:      d.Source,
		ContentType: SafeContentType(d.Kind, bareContentType(d.ContentType)),
		RequestID:   strings.TrimSpace(d.RequestID),
		UserPath:    strings.TrimSpace(d.UserPath),
		CreatedAt:   now,
	}
	if d.TTL > 0 {
		object.ExpiresAt = now.Add(d.TTL)
	}
	object.StorageKey = storageKey(object)
	if _, err := normalizeObject(&object); err != nil {
		return nil, err
	}
	writer, err := s.blobs.Create(ctx, object.StorageKey)
	if err != nil {
		return nil, fmt.Errorf("create media blob: %w", err)
	}
	return &Upload{service: s, writer: writer, object: object}, nil
}

// Write appends to the blob.
func (u *Upload) Write(p []byte) (int, error) {
	if u.done {
		return 0, errors.New("write to finished media upload")
	}
	n, err := u.writer.Write(p)
	u.object.Bytes += int64(n)
	return n, err
}

// Commit publishes the blob and writes its record. A record that cannot be
// written takes the blob with it, so the two never disagree.
func (u *Upload) Commit(ctx context.Context) (*Object, error) {
	if u.done {
		return nil, errors.New("commit of finished media upload")
	}
	u.done = true
	if err := u.writer.Commit(); err != nil {
		return nil, u.service.abandon(ctx, u.object, fmt.Errorf("commit media blob: %w", err))
	}
	if err := u.service.objects.Insert(ctx, &u.object); err != nil {
		return nil, u.service.abandon(ctx, u.object, fmt.Errorf("record media object: %w", err))
	}
	object := u.object
	return &object, nil
}

// Close discards an uncommitted upload; after Commit it is a no-op.
func (u *Upload) Close() error {
	if u.done {
		return nil
	}
	u.done = true
	return u.writer.Close()
}

// abandon removes whatever a failed commit may have published under the
// upload's key. The key was minted for this upload alone, so nothing else
// can be there. If the blob cannot be removed now, its record is written
// already expired, so the retention sweep keeps retrying the removal
// instead of the blob being orphaned.
func (s *Service) abandon(ctx context.Context, object Object, cause error) error {
	deleteErr := s.blobs.Delete(ctx, object.StorageKey)
	if deleteErr == nil {
		return cause
	}
	object.ExpiresAt = s.now().UTC()
	if insertErr := s.objects.Insert(ctx, &object); insertErr != nil {
		return errors.Join(cause, fmt.Errorf("remove media blob: %w", deleteErr), fmt.Errorf("record media blob for retention: %w", insertErr))
	}
	return errors.Join(cause, fmt.Errorf("remove media blob (left for the retention sweep): %w", deleteErr))
}

// Put stores everything r yields as one object.
func (s *Service) Put(ctx context.Context, d Descriptor, r io.Reader) (*Object, error) {
	upload, err := s.Begin(ctx, d)
	if err != nil {
		return nil, err
	}
	defer func() { _ = upload.Close() }()
	if _, err := io.Copy(upload, r); err != nil {
		return nil, fmt.Errorf("write media blob: %w", err)
	}
	return upload.Commit(ctx)
}

// Get returns an object's record, or ErrNotFound once it has expired. A
// malformed id is not found without touching the store.
func (s *Service) Get(ctx context.Context, id string) (*Object, error) {
	id = strings.TrimSpace(id)
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	object, err := s.objects.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if object.Expired(s.now()) {
		return nil, ErrNotFound
	}
	return object, nil
}

// Open returns an object's record and a reader over its bytes. The caller
// closes the reader.
func (s *Service) Open(ctx context.Context, id string) (*Object, io.ReadSeekCloser, error) {
	object, err := s.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	reader, err := s.blobs.Open(ctx, object.StorageKey)
	if err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	return object, reader, nil
}

// Delete removes an object's bytes and record.
func (s *Service) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if !ValidID(id) {
		return ErrNotFound
	}
	object, err := s.objects.Get(ctx, id)
	if err != nil {
		return err
	}
	return s.remove(ctx, object)
}

func (s *Service) remove(ctx context.Context, object *Object) error {
	if err := s.blobs.Delete(ctx, object.StorageKey); err != nil {
		return fmt.Errorf("delete media blob: %w", err)
	}
	if err := s.objects.Delete(ctx, object.ID); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// Sweep removes every object whose retention has run out and reports how
// many it removed. An object that cannot be removed does not stop the
// others: the sweep works through the batch, then reports the failures
// together. It stops after a batch with failures rather than reselecting the
// same objects forever; the next hourly run tries them again. The hourly
// loop calls it; tests call it directly.
func (s *Service) Sweep(ctx context.Context) (int, error) {
	removed := 0
	for {
		expired, err := s.objects.Expired(ctx, s.now(), sweepBatch)
		if err != nil {
			return removed, err
		}
		var failures []error
		for _, object := range expired {
			if err := s.remove(ctx, object); err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", object.ID, err))
				continue
			}
			removed++
		}
		if len(failures) > 0 {
			return removed, fmt.Errorf("%d of %d expired media objects not removed: %w", len(failures), len(expired), errors.Join(failures...))
		}
		if len(expired) < sweepBatch {
			return removed, nil
		}
	}
}

func (s *Service) sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	removed, err := s.Sweep(ctx)
	if err != nil {
		slog.Warn("media retention sweep failed", "removed", removed, "error", err)
	} else if removed > 0 {
		slog.Debug("media retention sweep removed expired objects", "removed", removed)
	}
}

// Close stops the sweep and closes both stores.
func (s *Service) Close() error {
	var errs []error
	s.closeOnce.Do(func() {
		close(s.stop)
		if err := s.objects.Close(); err != nil {
			errs = append(errs, fmt.Errorf("media records close: %w", err))
		}
		if err := s.blobs.Close(); err != nil {
			errs = append(errs, fmt.Errorf("media blobs close: %w", err))
		}
	})
	return errors.Join(errs...)
}

func newObjectID() string {
	return "med_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// storageKey lays blobs out as <kind>/<yyyy>/<mm>/<dd>/<id>.<ext>, which an
// operator can browse on a mounted volume without the database.
func storageKey(object Object) string {
	return string(object.Kind) + "/" + object.CreatedAt.Format("2006/01/02") + "/" + object.ID + "." + fileExtension(object.ContentType)
}

// fileExtension picks the conventional extension for the media types the
// gateway relays; anything else is .bin. The mime package is not used
// because its answers depend on the host's mime.types files.
func fileExtension(contentType string) string {
	switch contentType {
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	case "audio/ogg":
		return "ogg"
	case "audio/flac", "audio/x-flac":
		return "flac"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "m4a"
	case "audio/aac":
		return "aac"
	case "audio/webm":
		return "webm"
	case "audio/opus":
		return "opus"
	case "audio/pcm", "audio/l16":
		return "pcm"
	case "image/png":
		return "png"
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	case "video/mp4":
		return "mp4"
	case "video/webm":
		return "webm"
	default:
		return "bin"
	}
}

// bareContentType strips MIME parameters and normalizes case so the stored
// type works in a Content-Type header and a data: URL alike.
func bareContentType(contentType string) string {
	mediaType, _, _ := strings.Cut(contentType, ";")
	return strings.ToLower(strings.TrimSpace(mediaType))
}
