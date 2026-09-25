package admin

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/blobstore"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/mediastore"
)

func newMediaTestStore(t *testing.T) *mediastore.Service {
	t.Helper()
	store := mediastore.NewService(mediastore.NewMemoryStore(), blobstore.NewMemory())
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func storeTestMedia(t *testing.T, store *mediastore.Service, userPath, data string) *mediastore.Object {
	t.Helper()
	object, err := store.Put(context.Background(), mediastore.Descriptor{
		Kind:        mediastore.KindAudio,
		Source:      mediastore.SourceAudit,
		ContentType: "audio/mpeg",
		UserPath:    userPath,
	}, strings.NewReader(data))
	require.NoError(t, err)
	return object
}

func TestMedia_Unavailable(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Get(t, "/admin/media/med_1", echotest.WithPathValue("id", "med_1"))
	require.NoError(t, h.Media(c))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
}

func TestMedia_MissingID(t *testing.T) {
	h := NewHandler(nil, nil, WithMediaStore(newMediaTestStore(t)))
	c, rec := echotest.Get(t, "/admin/media/", echotest.WithPathValue("id", " "))
	require.NoError(t, h.Media(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestMedia_NotFound(t *testing.T) {
	h := NewHandler(nil, nil, WithMediaStore(newMediaTestStore(t)))
	c, rec := echotest.Get(t, "/admin/media/med_missing", echotest.WithPathValue("id", "med_missing"))
	require.NoError(t, h.Media(c))
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "media_not_found")
}

func TestMedia_ServesBytesWithContentType(t *testing.T) {
	store := newMediaTestStore(t)
	object := storeTestMedia(t, store, "/team/a", "synthetic-audio")
	h := NewHandler(nil, nil, WithMediaStore(store))

	c, rec := echotest.Get(t, "/admin/media/"+object.ID, echotest.WithPathValue("id", object.ID))
	require.NoError(t, h.Media(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "synthetic-audio", rec.Body.String())
	assert.Equal(t, "audio/mpeg", rec.Header().Get("Content-Type"))
	assert.Equal(t, "bytes", rec.Header().Get("Accept-Ranges"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "inline", rec.Header().Get("Content-Disposition"))
}

func TestMedia_HonorsRangeRequests(t *testing.T) {
	store := newMediaTestStore(t)
	object := storeTestMedia(t, store, "/team/a", "synthetic-audio")
	h := NewHandler(nil, nil, WithMediaStore(store))

	c, rec := echotest.Get(t, "/admin/media/"+object.ID,
		echotest.WithPathValue("id", object.ID),
		echotest.WithHeader("Range", "bytes=10-14"))
	require.NoError(t, h.Media(c))
	require.Equal(t, http.StatusPartialContent, rec.Code, rec.Body.String())
	assert.Equal(t, "audio", rec.Body.String())
	assert.Equal(t, "bytes 10-14/15", rec.Header().Get("Content-Range"))
}

func TestMedia_ScopeOutsideReadsAsMissing(t *testing.T) {
	store := newMediaTestStore(t)
	object := storeTestMedia(t, store, "/team/a", "x")
	h := NewHandler(nil, nil, WithMediaStore(store))

	tests := []struct {
		name  string
		scope string
		want  int
	}{
		{"global", "", http.StatusOK},
		{"same path", "/team/a", http.StatusOK},
		{"ancestor", "/team", http.StatusOK},
		{"sibling", "/team/b", http.StatusNotFound},
		{"descendant", "/team/a/x", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, rec := echotest.Get(t, "/admin/media/"+object.ID, echotest.WithPathValue("id", object.ID))
			req := c.Request()
			c.SetRequest(req.WithContext(core.WithAccessScope(req.Context(), core.AccessScope{UserPath: tt.scope})))
			require.NoError(t, h.Media(c))
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
		})
	}
}

func TestMedia_ObjectWithoutUserPathIsGlobalOnly(t *testing.T) {
	store := newMediaTestStore(t)
	object := storeTestMedia(t, store, "", "x")
	h := NewHandler(nil, nil, WithMediaStore(store))

	c, rec := echotest.Get(t, "/admin/media/"+object.ID, echotest.WithPathValue("id", object.ID))
	req := c.Request()
	c.SetRequest(req.WithContext(core.WithAccessScope(req.Context(), core.AccessScope{UserPath: "/team"})))
	require.NoError(t, h.Media(c))
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestMedia_NoBrowserCacheAndAttachmentForUntrustedTypes(t *testing.T) {
	store := newMediaTestStore(t)
	audio := storeTestMedia(t, store, "/team/a", "synthetic-audio")
	html, err := store.Put(context.Background(), mediastore.Descriptor{
		Kind:        mediastore.KindImage,
		Source:      mediastore.SourceAudit,
		ContentType: "text/html",
	}, strings.NewReader("<script>alert(1)</script>"))
	require.NoError(t, err)
	h := NewHandler(nil, nil, WithMediaStore(store))

	c, rec := echotest.Get(t, "/admin/media/"+audio.ID, echotest.WithPathValue("id", audio.ID))
	require.NoError(t, h.Media(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "inline", rec.Header().Get("Content-Disposition"))

	c, rec = echotest.Get(t, "/admin/media/"+html.ID, echotest.WithPathValue("id", html.ID))
	require.NoError(t, h.Media(c))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"), "a client-declared active type is never stored as such")
	assert.Equal(t, `attachment; filename="`+html.ID+`"`, rec.Header().Get("Content-Disposition"))
}
