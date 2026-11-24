package dashboardlogin

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	json "github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/echotest"
)

type recordedEvents struct {
	events []ext.AuthenticationEvent
}

func (r *recordedEvents) RecordAuthenticationEvent(event ext.AuthenticationEvent) {
	r.events = append(r.events, event)
}

func newTestHandler(t *testing.T, basePath string) (*Handler, *recordedEvents) {
	t.Helper()
	service := newTestService(t)
	events := &recordedEvents{}
	service.SetAuthenticationEventRecorder(events)
	return NewHandler(service, basePath), events
}

func signIn(t *testing.T, h *Handler, body any, opts ...echotest.Option) *httptest.ResponseRecorder {
	t.Helper()
	c, rec := echotest.Request(t, http.MethodPost, SessionPath, body, opts...)
	require.NoError(t, h.SignIn(c))
	return rec
}

func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == CookieName {
			return cookie
		}
	}
	require.FailNow(t, "response sets no session cookie")
	return nil
}

func decodeSession(t *testing.T, rec *httptest.ResponseRecorder) sessionResponse {
	t.Helper()
	var response sessionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	return response
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	return envelope.Error.Code
}

func TestSignInStartsASession(t *testing.T) {
	h, events := newTestHandler(t, "/")

	rec := signIn(t, h, credentials{Username: testUsername, Password: testPassword})

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	response := decodeSession(t, rec)
	assert.True(t, response.Authenticated)
	assert.Equal(t, testUsername, response.Username)
	require.NotNil(t, response.ExpiresAt)
	assert.WithinDuration(t, time.Now().Add(SessionTTL), *response.ExpiresAt, time.Minute)

	cookie := sessionCookie(t, rec)
	assert.True(t, cookie.HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	assert.Equal(t, "/", cookie.Path)
	assert.Equal(t, int(SessionTTL.Seconds()), cookie.MaxAge)
	assert.False(t, cookie.Secure, "a plain HTTP sign-in must get a cookie the browser sends back")
	_, ok := h.service.verify(cookie.Value)
	assert.True(t, ok)

	require.Len(t, events.events, 1)
	assert.Equal(t, "login", events.events[0].Type)
	assert.Equal(t, "success", events.events[0].Outcome)
	assert.Equal(t, testUsername, events.events[0].PrincipalID)
	assert.Equal(t, AuthMethod, events.events[0].Method)
}

func TestSignInCookieFollowsTheDeployment(t *testing.T) {
	h, _ := newTestHandler(t, "/gateway/")

	rec := signIn(t, h, credentials{Username: testUsername, Password: testPassword},
		echotest.WithRemoteAddr("127.0.0.1:4321"), echotest.WithHeader("X-Forwarded-Proto", "https"))

	cookie := sessionCookie(t, rec)
	assert.Equal(t, "/gateway", cookie.Path)
	assert.True(t, cookie.Secure)
}

func TestSignInRejectsBadRequests(t *testing.T) {
	tests := []struct {
		name     string
		body     any
		opts     []echotest.Option
		want     int
		wantCode string
		events   int
	}{
		{name: "wrong password", body: credentials{Username: testUsername, Password: "wrong"}, want: http.StatusUnauthorized, wantCode: "invalid_sign_in", events: 1},
		{name: "wrong username", body: credentials{Username: "root", Password: testPassword}, want: http.StatusUnauthorized, wantCode: "invalid_sign_in", events: 1},
		{name: "empty credentials", body: credentials{}, want: http.StatusUnauthorized, wantCode: "invalid_sign_in", events: 1},
		{name: "not JSON", body: "username=admin", want: http.StatusBadRequest},
		{name: "oversized body", body: `{"username":"` + strings.Repeat("a", maxCredentialsBytes) + `"}`, want: http.StatusBadRequest},
		{
			name: "another site", body: credentials{Username: testUsername, Password: testPassword},
			opts: []echotest.Option{echotest.WithHeader("Sec-Fetch-Site", "cross-site")},
			want: http.StatusForbidden, wantCode: "cross_origin_sign_in",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, events := newTestHandler(t, "/")

			rec := signIn(t, h, tt.body, tt.opts...)

			assert.Equal(t, tt.want, rec.Code)
			assert.Equal(t, tt.wantCode, errorCode(t, rec))
			assert.Empty(t, rec.Result().Cookies())
			assert.NotContains(t, rec.Body.String(), testPassword)
			require.Len(t, events.events, tt.events)
			for _, event := range events.events {
				assert.Equal(t, "failure", event.Outcome)
				assert.Empty(t, event.PrincipalID, "a failed sign-in records no submitted username")
			}
		})
	}
}

func TestSignInLocksOutAClientAfterRepeatedFailures(t *testing.T) {
	h, events := newTestHandler(t, "/")
	attacker := echotest.WithRemoteAddr("203.0.113.7:4321")
	wrong := credentials{Username: testUsername, Password: "wrong"}
	right := credentials{Username: testUsername, Password: testPassword}

	for range maxFailedAttempts {
		require.Equal(t, http.StatusUnauthorized, signIn(t, h, wrong, attacker).Code)
	}

	rec := signIn(t, h, right, attacker)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, "a locked-out client is refused even with the right password")
	assert.Equal(t, "too_many_sign_in_attempts", errorCode(t, rec))
	retryAfter, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	require.NoError(t, err)
	assert.InDelta(t, attemptWindow.Seconds(), retryAfter, 5)
	assert.Empty(t, rec.Result().Cookies())
	assert.Len(t, events.events, maxFailedAttempts, "a refused attempt adds nothing to the request log")

	assert.Equal(t, http.StatusOK, signIn(t, h, right, echotest.WithRemoteAddr("198.51.100.9:4321")).Code,
		"another client still signs in")
}

func TestSignInSuccessForgetsEarlierFailures(t *testing.T) {
	h, _ := newTestHandler(t, "/")
	wrong := credentials{Username: testUsername, Password: "wrong"}
	right := credentials{Username: testUsername, Password: testPassword}

	for range maxFailedAttempts - 1 {
		require.Equal(t, http.StatusUnauthorized, signIn(t, h, wrong).Code)
	}
	require.Equal(t, http.StatusOK, signIn(t, h, right).Code)
	for range maxFailedAttempts - 1 {
		require.Equal(t, http.StatusUnauthorized, signIn(t, h, wrong).Code)
	}

	assert.Equal(t, http.StatusOK, signIn(t, h, right).Code)
}

func TestSessionReportsTheCallersSession(t *testing.T) {
	h, _ := newTestHandler(t, "/")
	token, expires := h.service.issue()

	t.Run("signed in", func(t *testing.T) {
		c, rec := echotest.Request(t, http.MethodGet, SessionPath, nil, echotest.WithHeader("Cookie", CookieName+"="+token))
		require.NoError(t, h.Session(c))

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		response := decodeSession(t, rec)
		assert.True(t, response.Authenticated)
		assert.Equal(t, testUsername, response.Username)
		require.NotNil(t, response.ExpiresAt)
		assert.True(t, expires.Equal(*response.ExpiresAt))
	})

	for name, cookie := range map[string]string{"signed out": "", "invalid session": CookieName + "=forged"} {
		t.Run(name, func(t *testing.T) {
			var opts []echotest.Option
			if cookie != "" {
				opts = append(opts, echotest.WithHeader("Cookie", cookie))
			}
			c, rec := echotest.Request(t, http.MethodGet, SessionPath, nil, opts...)
			require.NoError(t, h.Session(c))

			require.Equal(t, http.StatusOK, rec.Code)
			assert.JSONEq(t, `{"authenticated":false}`, rec.Body.String())
		})
	}
}

func TestSignOutClearsTheSession(t *testing.T) {
	h, events := newTestHandler(t, "/gateway")
	token, _ := h.service.issue()

	c, rec := echotest.Request(t, http.MethodDelete, SessionPath, nil, echotest.WithHeader("Cookie", CookieName+"="+token))
	require.NoError(t, h.SignOut(c))

	require.Equal(t, http.StatusNoContent, rec.Code)
	cookie := sessionCookie(t, rec)
	assert.Empty(t, cookie.Value)
	assert.Negative(t, cookie.MaxAge)
	assert.Equal(t, "/gateway", cookie.Path, "the clearing cookie must match the one it replaces")
	require.Len(t, events.events, 1)
	assert.Equal(t, "logout", events.events[0].Type)

	c, rec = echotest.Request(t, http.MethodDelete, SessionPath, nil)
	require.NoError(t, h.SignOut(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Len(t, events.events, 1, "signing out without a session records nothing")
}
