package mediastore

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/blobstore"
)

// newTestService builds a service without the background sweep, so a test
// can move the returned clock freely and call Sweep itself.
func newTestService(t *testing.T) (*Service, *blobstore.Memory, *time.Time) {
	t.Helper()
	blobs := blobstore.NewMemory()
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	svc := newService(NewMemoryStore(), blobs, func() time.Time { return now }, false)
	t.Cleanup(func() { _ = svc.Close() })
	return svc, blobs, &now
}

func readObject(t *testing.T, svc *Service, id string) (*Object, string) {
	t.Helper()
	object, reader, err := svc.Open(context.Background(), id)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	return object, string(data)
}

func TestService_PutAndOpen(t *testing.T) {
	svc, blobs, now := newTestService(t)
	*now = time.Date(2026, 9, 22, 10, 30, 0, 0, time.UTC)

	object, err := svc.Put(context.Background(), Descriptor{
		Kind:        KindAudio,
		Source:      SourceAudit,
		ContentType: "Audio/MPEG; codecs=mp3",
		RequestID:   "req-1",
		UserPath:    "/team",
		TTL:         30 * 24 * time.Hour,
	}, strings.NewReader("synthetic-audio"))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(object.ID, "med_"), "id = %q", object.ID)
	assert.Equal(t, "audio/mpeg", object.ContentType)
	assert.Equal(t, int64(len("synthetic-audio")), object.Bytes)
	assert.Equal(t, "audio/2026/09/22/"+object.ID+".mp3", object.StorageKey)
	assert.Equal(t, "req-1", object.RequestID)
	assert.Equal(t, "/team", object.UserPath)
	assert.True(t, object.ExpiresAt.Equal(time.Date(2026, 10, 22, 10, 30, 0, 0, time.UTC)), "expires_at = %s", object.ExpiresAt)
	assert.Equal(t, 1, blobs.Len())

	got, data := readObject(t, svc, object.ID)
	assert.Equal(t, object.ID, got.ID)
	assert.Equal(t, "synthetic-audio", data)
}

func TestService_UploadCloseWithoutCommitLeavesNothing(t *testing.T) {
	svc, blobs, _ := newTestService(t)
	ctx := context.Background()
	upload, err := svc.Begin(ctx, Descriptor{Kind: KindImage, Source: SourceAudit, ContentType: "image/png"})
	require.NoError(t, err)
	_, err = upload.Write([]byte("partial"))
	require.NoError(t, err)
	require.NoError(t, upload.Close())

	assert.Equal(t, 0, blobs.Len())
	_, err = svc.Get(ctx, upload.object.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = upload.Write([]byte("x"))
	require.Error(t, err)
	_, err = upload.Commit(ctx)
	assert.Error(t, err)
}

func TestService_CommitFailureRemovesBlob(t *testing.T) {
	blobs := blobstore.NewMemory()
	svc := NewService(failingStore{}, blobs)
	t.Cleanup(func() { _ = svc.Close() })

	_, err := svc.Put(context.Background(), Descriptor{Kind: KindAudio, Source: SourceAudit, ContentType: "audio/wav"}, strings.NewReader("x"))
	require.Error(t, err)
	assert.Equal(t, 0, blobs.Len(), "a blob without a record must not be left behind")
}

func TestService_ExpiredObjectReadsAsMissing(t *testing.T) {
	svc, _, now := newTestService(t)
	ctx := context.Background()

	object, err := svc.Put(ctx, Descriptor{Kind: KindAudio, Source: SourceAudit, ContentType: "audio/wav", TTL: time.Hour}, strings.NewReader("x"))
	require.NoError(t, err)
	_, err = svc.Get(ctx, object.ID)
	require.NoError(t, err)

	*now = now.Add(time.Hour)
	_, err = svc.Get(ctx, object.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, _, err = svc.Open(ctx, object.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestService_SweepRemovesExpiredRecordsAndBlobs(t *testing.T) {
	svc, blobs, now := newTestService(t)
	ctx := context.Background()

	short, err := svc.Put(ctx, Descriptor{Kind: KindAudio, Source: SourceAudit, ContentType: "audio/wav", TTL: time.Hour}, strings.NewReader("short"))
	require.NoError(t, err)
	long, err := svc.Put(ctx, Descriptor{Kind: KindAudio, Source: SourceAudit, ContentType: "audio/wav", TTL: 48 * time.Hour}, strings.NewReader("long"))
	require.NoError(t, err)
	forever, err := svc.Put(ctx, Descriptor{Kind: KindImage, Source: SourceAudit, ContentType: "image/png"}, strings.NewReader("forever"))
	require.NoError(t, err)
	require.Equal(t, 3, blobs.Len())

	*now = now.Add(2 * time.Hour)
	removed, err := svc.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	assert.Equal(t, 2, blobs.Len())
	_, err = svc.objects.Get(ctx, short.ID)
	require.ErrorIs(t, err, ErrNotFound, "expired record must be deleted, not just hidden")
	_, err = svc.Get(ctx, long.ID)
	require.NoError(t, err)
	_, err = svc.Get(ctx, forever.ID)
	require.NoError(t, err)

	// A blob already gone from the store does not stall the sweep.
	*now = now.Add(72 * time.Hour)
	require.NoError(t, blobs.Delete(ctx, long.StorageKey))
	removed, err = svc.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	_, err = svc.objects.Get(ctx, long.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestService_SweepWorksThroughBatches(t *testing.T) {
	svc, blobs, now := newTestService(t)
	ctx := context.Background()
	for range sweepBatch + 3 {
		_, err := svc.Put(ctx, Descriptor{Kind: KindAudio, Source: SourceAudit, ContentType: "audio/wav", TTL: time.Minute}, strings.NewReader("x"))
		require.NoError(t, err)
	}
	*now = now.Add(time.Hour)
	removed, err := svc.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, sweepBatch+3, removed)
	assert.Equal(t, 0, blobs.Len())
}

func TestService_Delete(t *testing.T) {
	svc, blobs, _ := newTestService(t)
	ctx := context.Background()
	object, err := svc.Put(ctx, Descriptor{Kind: KindAudio, Source: SourceAudit, ContentType: "audio/wav"}, strings.NewReader("x"))
	require.NoError(t, err)
	require.NoError(t, svc.Delete(ctx, object.ID))
	assert.Equal(t, 0, blobs.Len())
	assert.ErrorIs(t, svc.Delete(ctx, object.ID), ErrNotFound)
}

func TestService_MissingBlobReadsAsMissing(t *testing.T) {
	svc, blobs, _ := newTestService(t)
	ctx := context.Background()
	object, err := svc.Put(ctx, Descriptor{Kind: KindAudio, Source: SourceAudit, ContentType: "audio/wav"}, strings.NewReader("x"))
	require.NoError(t, err)
	require.NoError(t, blobs.Delete(ctx, object.StorageKey))
	_, _, err = svc.Open(ctx, object.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestFileExtension(t *testing.T) {
	tests := map[string]string{
		"audio/mpeg": "mp3",
		"audio/wav":  "wav",
		"audio/mp4":  "m4a",
		"image/png":  "png",
		"image/jpeg": "jpg",
		"video/mp4":  "mp4",
		"":           "bin",
		"text/plain": "bin",
	}
	for contentType, want := range tests {
		assert.Equal(t, want, fileExtension(contentType), "fileExtension(%q)", contentType)
	}
}

func TestOpenBlobStore(t *testing.T) {
	fs, err := OpenBlobStore("filesystem", filepath.Join(t.TempDir(), "media"))
	require.NoError(t, err)
	assert.IsType(t, &blobstore.Filesystem{}, fs)

	defaulted, err := OpenBlobStore("", filepath.Join(t.TempDir(), "media"))
	require.NoError(t, err)
	assert.IsType(t, &blobstore.Filesystem{}, defaulted)

	mem, err := OpenBlobStore("MEMORY", "")
	require.NoError(t, err)
	assert.IsType(t, &blobstore.Memory{}, mem)

	_, err = OpenBlobStore("s3", "")
	assert.Error(t, err)
}

// failingStore refuses every record so commit-time cleanup can be observed.
type failingStore struct{}

func (failingStore) Insert(context.Context, *Object) error { return assert.AnError }
func (failingStore) Get(context.Context, string) (*Object, error) {
	return nil, ErrNotFound
}
func (failingStore) Delete(context.Context, string) error { return ErrNotFound }
func (failingStore) Expired(context.Context, time.Time, int) ([]*Object, error) {
	return nil, nil
}
func (failingStore) Close() error { return nil }

func TestValidID(t *testing.T) {
	svc, _, _ := newTestService(t)
	object, err := svc.Put(context.Background(), Descriptor{Kind: KindAudio, Source: SourceAudit, ContentType: "audio/wav"}, strings.NewReader("x"))
	require.NoError(t, err)
	assert.True(t, ValidID(object.ID), "issued id %q must validate", object.ID)

	for _, id := range []string{"", "med_", "med_missing", "MED_" + strings.Repeat("a", 32), "med_" + strings.Repeat("a", 31), "med_" + strings.Repeat("a", 33), "../etc/passwd", `{"$ne": null}`} {
		assert.False(t, ValidID(id), "ValidID(%q)", id)
		_, err := svc.Get(context.Background(), id)
		require.ErrorIs(t, err, ErrNotFound, "Get(%q)", id)
		assert.ErrorIs(t, svc.Delete(context.Background(), id), ErrNotFound, "Delete(%q)", id)
	}
}

// stickyBlobs refuses to delete one key so a sweep can be watched working
// past it.
type stickyBlobs struct {
	*blobstore.Memory
	stuck string
}

func (b stickyBlobs) Delete(ctx context.Context, key string) error {
	if key == b.stuck {
		return assert.AnError
	}
	return b.Memory.Delete(ctx, key)
}

func TestService_SweepContinuesPastAFailedObject(t *testing.T) {
	blobs := blobstore.NewMemory()
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	sticky := &stickyBlobs{Memory: blobs}
	svc := newService(NewMemoryStore(), sticky, func() time.Time { return now }, false)
	t.Cleanup(func() { _ = svc.Close() })
	ctx := context.Background()

	oldest, err := svc.Put(ctx, Descriptor{Kind: KindAudio, Source: SourceAudit, ContentType: "audio/wav", TTL: time.Minute}, strings.NewReader("oldest"))
	require.NoError(t, err)
	sticky.stuck = oldest.StorageKey
	later, err := svc.Put(ctx, Descriptor{Kind: KindAudio, Source: SourceAudit, ContentType: "audio/wav", TTL: 2 * time.Minute}, strings.NewReader("later"))
	require.NoError(t, err)

	now = now.Add(time.Hour)
	removed, err := svc.Sweep(ctx)
	require.Error(t, err, "the stuck object is reported")
	assert.Equal(t, 1, removed)
	_, err = svc.objects.Get(ctx, later.ID)
	require.ErrorIs(t, err, ErrNotFound, "the later object is removed despite the earlier failure")
	_, err = svc.objects.Get(ctx, oldest.ID)
	require.NoError(t, err, "the stuck object stays for the next sweep")

	sticky.stuck = ""
	removed, err = svc.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	assert.Equal(t, 0, blobs.Len())
}
