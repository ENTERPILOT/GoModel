package auditlog

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/blobstore"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/mediastore"
)

// newTestMediaCapture returns a capture bound to a request that carries a
// live audit entry, backed by in-memory media stores.
func newTestMediaCapture(t *testing.T, retentionDays int) (*MediaCapture, *mediastore.Service) {
	t.Helper()
	store := mediastore.NewService(mediastore.NewMemoryStore(), blobstore.NewMemory())
	t.Cleanup(func() { _ = store.Close() })
	c, _ := echotest.Post(t, "/v1/audio/speech", `{}`)
	c.Set(string(LogEntryKey), &LogEntry{RequestID: "req-1", UserPath: "/team/a"})
	capture := NewMediaCapturer(store, retentionDays).For(c)
	require.NotNil(t, capture)
	return capture, store
}

func readMedia(t *testing.T, store *mediastore.Service, id string) (*mediastore.Object, []byte) {
	t.Helper()
	object, reader, err := store.Open(context.Background(), id)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	return object, data
}

func TestNewMediaCapturer_NilStoreIsNil(t *testing.T) {
	capturer := NewMediaCapturer(nil, 30)
	assert.Nil(t, capturer)
	c, _ := echotest.Post(t, "/v1/audio/speech", `{}`)
	assert.Nil(t, capturer.For(c), "a nil capturer binds to nothing")
}

func TestMediaCapturer_ForRequiresAuditEntry(t *testing.T) {
	store := mediastore.NewService(mediastore.NewMemoryStore(), blobstore.NewMemory())
	t.Cleanup(func() { _ = store.Close() })
	c, _ := echotest.Post(t, "/v1/audio/speech", `{}`)
	assert.Nil(t, NewMediaCapturer(store, 30).For(c), "no audit entry means nothing could reference the media")
}

func TestMediaCapture_SaveCarriesOwnerAndRetention(t *testing.T) {
	capture, store := newTestMediaCapture(t, 30)
	body := BuildAudioResponseBody(capture, "audio/mpeg; codecs=mp3", []byte{0xff, 0xfb, 0x90, 0x00})
	require.True(t, body.Stored)
	require.NotEmpty(t, body.MediaID)

	object, data := readMedia(t, store, body.MediaID)
	assert.Equal(t, []byte{0xff, 0xfb, 0x90, 0x00}, data, "bytes must survive verbatim, not as UTF-8")
	assert.Equal(t, mediastore.KindAudio, object.Kind)
	assert.Equal(t, mediastore.SourceAudit, object.Source)
	assert.Equal(t, "audio/mpeg", object.ContentType)
	assert.Equal(t, "req-1", object.RequestID)
	assert.Equal(t, "/team/a", object.UserPath)
	assert.WithinDuration(t, time.Now().Add(30*24*time.Hour), object.ExpiresAt, time.Minute)
}

func TestMediaCapture_ZeroRetentionKeepsForever(t *testing.T) {
	capture, store := newTestMediaCapture(t, 0)
	body := BuildAudioResponseBody(capture, "audio/wav", []byte("x"))
	object, _ := readMedia(t, store, body.MediaID)
	assert.True(t, object.ExpiresAt.IsZero(), "expires_at = %s", object.ExpiresAt)
}

func TestMediaWriter_StoresRelayedBytes(t *testing.T) {
	capture, store := newTestMediaCapture(t, 30)
	w := capture.AudioWriter("audio/mpeg")
	for _, chunk := range []string{"synthetic-", "audio"} {
		n, err := w.Write([]byte(chunk))
		require.NoError(t, err)
		assert.Equal(t, len(chunk), n)
	}
	body := BuildRelayedAudioResponseBody(w)
	require.True(t, body.Stored)
	assert.Equal(t, int64(len("synthetic-audio")), body.Bytes)
	assert.Equal(t, "audio/mpeg", body.ContentType)
	_, data := readMedia(t, store, body.MediaID)
	assert.Equal(t, "synthetic-audio", string(data))

	again := BuildRelayedAudioResponseBody(w)
	assert.Equal(t, body.MediaID, again.MediaID, "a second build reuses the committed object")
}

func TestMediaWriter_NilCaptureOnlyCounts(t *testing.T) {
	var capture *MediaCapture
	w := capture.AudioWriter("audio/mpeg")
	_, err := w.Write([]byte("abc"))
	require.NoError(t, err)
	body := BuildRelayedAudioResponseBody(w)
	assert.False(t, body.Stored)
	assert.Empty(t, body.MediaID)
	assert.Equal(t, int64(3), body.Bytes)
	assert.True(t, body.Audio)
}

func TestMediaWriter_EmptyRelayStoresNothing(t *testing.T) {
	capture, store := newTestMediaCapture(t, 30)
	w := capture.AudioWriter("audio/mpeg")
	body := BuildRelayedAudioResponseBody(w)
	assert.False(t, body.Stored)
	assert.Equal(t, int64(0), body.Bytes)
	_, err := store.Sweep(context.Background())
	require.NoError(t, err)
}

func TestMediaWriter_StoreFailureNeverBreaksTheRelay(t *testing.T) {
	store := mediastore.NewService(mediastore.NewMemoryStore(), failingBlobs{})
	t.Cleanup(func() { _ = store.Close() })
	c, _ := echotest.Post(t, "/v1/audio/speech", `{}`)
	c.Set(string(LogEntryKey), &LogEntry{RequestID: "req-1"})
	capture := NewMediaCapturer(store, 30).For(c)

	w := capture.AudioWriter("audio/mpeg")
	n, err := w.Write([]byte("chunk"))
	require.NoError(t, err, "a failing store must not surface to the relay")
	assert.Equal(t, 5, n)
	body := BuildRelayedAudioResponseBody(w)
	assert.False(t, body.Stored)
	assert.Equal(t, int64(5), body.Bytes)

	buffered := BuildAudioResponseBody(capture, "audio/mpeg", []byte("chunk"))
	assert.False(t, buffered.Stored)
	assert.Equal(t, int64(5), buffered.Bytes)
}

func TestMediaCapture_ContextSurvivesRequestCancellation(t *testing.T) {
	store := mediastore.NewService(mediastore.NewMemoryStore(), blobstore.NewMemory())
	t.Cleanup(func() { _ = store.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	c, _ := echotest.Post(t, "/v1/audio/speech", `{}`)
	c.SetRequest(c.Request().WithContext(ctx))
	c.Set(string(LogEntryKey), &LogEntry{RequestID: "req-1"})
	capture := NewMediaCapturer(store, 30).For(c)
	cancel()

	body := BuildAudioResponseBody(capture, "audio/mpeg", []byte("late"))
	assert.True(t, body.Stored, "a client that went away must not cancel the store")
}

// failingBlobs refuses every write so store failures can be observed.
type failingBlobs struct{}

func (failingBlobs) Create(context.Context, string) (blobstore.Writer, error) {
	return failingWriter{}, nil
}
func (failingBlobs) Open(context.Context, string) (io.ReadSeekCloser, error) {
	return nil, blobstore.ErrNotFound
}
func (failingBlobs) Delete(context.Context, string) error { return nil }
func (failingBlobs) Close() error                         { return nil }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, assert.AnError }
func (failingWriter) Commit() error             { return assert.AnError }
func (failingWriter) Close() error              { return nil }
