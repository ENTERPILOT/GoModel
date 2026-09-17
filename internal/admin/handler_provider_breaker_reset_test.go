package admin

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/providers"
)

// breakerResetterFake records reset calls and replays a canned error.
type breakerResetterFake struct {
	calls []string
	err   error
}

func (f *breakerResetterFake) ResetCircuitBreaker(providerName string) error {
	f.calls = append(f.calls, providerName)
	return f.err
}

// resetBreakerRequest builds the handler call for POST .../circuit-breaker/reset.
func resetBreakerRequest(t *testing.T, name string, fake *breakerResetterFake) (*Handler, *echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	h := NewHandler(nil, nil, WithBreakerResetter(fake))
	c, rec := echotest.Post(t, "/admin/providers/"+name+"/circuit-breaker/reset", nil,
		echotest.WithPathValue("name", name))
	return h, c, rec
}

func TestResetProviderCircuitBreaker_Success(t *testing.T) {
	fake := &breakerResetterFake{}
	h, c, rec := resetBreakerRequest(t, "openai-main", fake)

	err := h.ResetProviderCircuitBreaker(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Body.String())
	assert.Equal(t, []string{"openai-main"}, fake.calls)
}

func TestResetProviderCircuitBreaker_UnknownProvider(t *testing.T) {
	fake := &breakerResetterFake{
		err: fmt.Errorf("%w: nope", providers.ErrProviderNotFound),
	}
	h, c, rec := resetBreakerRequest(t, "nope", fake)

	err := h.ResetProviderCircuitBreaker(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "provider not found")
}

func TestResetProviderCircuitBreaker_ProviderCannotReset(t *testing.T) {
	fake := &breakerResetterFake{
		err: errors.New(`provider "legacy" does not support circuit breaker reset`),
	}
	h, c, rec := resetBreakerRequest(t, "legacy", fake)

	err := h.ResetProviderCircuitBreaker(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "does not support circuit breaker reset")
}

func TestResetProviderCircuitBreaker_FeatureUnavailable(t *testing.T) {
	h := NewHandler(nil, nil)
	c, rec := echotest.Post(t, "/admin/providers/openai-main/circuit-breaker/reset", nil,
		echotest.WithPathValue("name", "openai-main"))

	err := h.ResetProviderCircuitBreaker(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
}
