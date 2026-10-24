//go:build !swagger

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSwaggerEndpoint_RequestedButNotAvailable(t *testing.T) {
	mock := &mockProvider{}
	srv := New(mock, &Config{SwaggerEnabled: true})

	req := httptest.NewRequest(http.MethodGet, "/swagger/index.html", nil)
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
