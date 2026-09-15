package providertest

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServer_RecordsRequestsAndKeepsBodyReadable(t *testing.T) {
	var seen string
	server, capture := Server(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = string(body)
		w.WriteHeader(http.StatusAccepted)
	})

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/x?q=1", strings.NewReader(`{"a":1}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer k")
	resp, err := server.Client().Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Equal(t, `{"a":1}`, seen)
	require.Equal(t, 1, capture.Count())
	last := capture.Last(t)
	assert.Equal(t, http.MethodPost, last.Method)
	assert.Equal(t, "/v1/x", last.Path)
	assert.Equal(t, "1", last.Query.Get("q"))
	assert.Equal(t, "Bearer k", last.Header.Get("Authorization"))
	assert.Equal(t, float64(1), last.JSON(t)["a"])
}

func TestRouteServer_DispatchesByPath(t *testing.T) {
	server, capture := RouteServer(t, map[string]http.HandlerFunc{
		"/ok": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
	})
	resp, err := http.Get(server.URL + "/ok")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	resp, err = http.Get(server.URL + "/missing")
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Len(t, capture.All(), 2)
}
