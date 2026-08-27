package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/versioncheck"
)

// versionTestServer wires a gateway whose update check reads manifests from a
// local test origin, and reports how many manifest requests it made.
func versionTestServer(t *testing.T, handler http.HandlerFunc) (*Server, *atomic.Int64, *http.Header) {
	t.Helper()
	var calls atomic.Int64
	var lastHeaders http.Header
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		lastHeaders = r.Header.Clone()
		handler(w, r)
	}))
	t.Cleanup(origin.Close)

	checker := versioncheck.New(versioncheck.Config{
		Enabled:   true,
		URL:       origin.URL + "/version",
		App:       "GoModel",
		Version:   "0.1.81",
		InstallID: "install-abc",
		Client:    origin.Client(),
	})
	return New(&mockProvider{}, &Config{VersionChecker: checker}), &calls, &lastHeaders
}

func manifestOK(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("0.1.82\n"))
}

// visitCookie returns the value the response set for the visit cookie.
func visitCookie(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == versioncheck.CookieName {
			return cookie.Value
		}
	}
	return ""
}

func decodeStatus(t *testing.T, rec *httptest.ResponseRecorder) versioncheck.Status {
	t.Helper()
	var status versioncheck.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode /version body %q: %v", rec.Body.String(), err)
	}
	return status
}

func TestVersionEndpointChecksOnFirstVisit(t *testing.T) {
	srv, calls, headers := versionTestServer(t, manifestOK)

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64)")
	req.Host = "gateway.example.com"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if calls.Load() != 1 {
		t.Fatalf("made %d manifest requests, want 1", calls.Load())
	}
	status := decodeStatus(t, rec)
	if status.Latest != "0.1.82" || !status.UpdateAvailable {
		t.Fatalf("got %+v, want the newer release reported", status)
	}
	if value := headers.Get("X-GoModel-Host"); value != "" {
		t.Errorf("X-GoModel-Host = %q, want the dashboard hostname kept local", value)
	}
	if value := headers.Get("X-Forwarded-For"); value != "" {
		t.Errorf("X-Forwarded-For = %q, want the client address kept local", value)
	}
	if headers.Get("User-Agent") != "Mozilla/5.0 (X11; Linux x86_64)" {
		t.Errorf("User-Agent = %q, want the browser's forwarded", headers.Get("User-Agent"))
	}

	date, id := versioncheck.SplitVisit(visitCookie(t, rec))
	if date != time.Now().Format(time.DateOnly) || id == "" {
		t.Fatalf("visit cookie = %q, want today plus a fresh id", visitCookie(t, rec))
	}
	if headers.Get("X-GoModel-Date") != visitCookie(t, rec) {
		t.Errorf("X-GoModel-Date = %q, want the cookie value %q", headers.Get("X-GoModel-Date"), visitCookie(t, rec))
	}
	if cache := rec.Header().Get("Cache-Control"); cache != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cache)
	}
}

func TestVersionEndpointSkipsSecondVisitSameDay(t *testing.T) {
	srv, calls, _ := versionTestServer(t, manifestOK)

	first := httptest.NewRequest(http.MethodGet, "/version", nil)
	firstRec := httptest.NewRecorder()
	srv.ServeHTTP(firstRec, first)

	second := httptest.NewRequest(http.MethodGet, "/version", nil)
	second.AddCookie(&http.Cookie{Name: versioncheck.CookieName, Value: visitCookie(t, firstRec)})
	secondRec := httptest.NewRecorder()
	srv.ServeHTTP(secondRec, second)

	if calls.Load() != 1 {
		t.Fatalf("made %d manifest requests, want the second visit served from cache", calls.Load())
	}
	if status := decodeStatus(t, secondRec); status.Latest != "0.1.82" {
		t.Fatalf("cached response lost the result: %+v", status)
	}
}

func TestVersionEndpointRechecksOnANewDay(t *testing.T) {
	srv, calls, _ := versionTestServer(t, manifestOK)

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	yesterday := time.Now().AddDate(0, 0, -1).Format(time.DateOnly)
	req.AddCookie(&http.Cookie{Name: versioncheck.CookieName, Value: yesterday + "-keep-me"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if calls.Load() != 1 {
		t.Fatalf("made %d manifest requests, want a fresh check for the new day", calls.Load())
	}
	date, id := versioncheck.SplitVisit(visitCookie(t, rec))
	if date != time.Now().Format(time.DateOnly) {
		t.Errorf("cookie date = %q, want it rolled to today", date)
	}
	if id != "keep-me" {
		t.Errorf("cookie id = %q, want the browser's id preserved across days", id)
	}
}

func TestVersionEndpointNeverForwardsCredentials(t *testing.T) {
	srv, _, headers := versionTestServer(t, manifestOK)

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	req.Header.Set("Authorization", "Bearer sk-master-key")
	req.Header.Set("X-API-Key", "sk-provider-key")
	req.Header.Set("Referer", "https://gateway.example.com/admin/dashboard?token=leak")
	req.AddCookie(&http.Cookie{Name: "gomodel_session", Value: "super-secret"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	for _, forbidden := range []string{"Authorization", "X-API-Key", "Referer", "Cookie"} {
		if value := headers.Get(forbidden); value != "" {
			t.Errorf("%s leaked to the release host as %q", forbidden, value)
		}
	}
	for _, value := range *headers {
		if strings.Contains(strings.Join(value, " "), "sk-") {
			t.Errorf("a credential-shaped value reached the release host: %v", value)
		}
	}
}

func TestVersionEndpointCookieIsReadableByTheDashboard(t *testing.T) {
	srv, _, _ := versionTestServer(t, manifestOK)

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == versioncheck.CookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no visit cookie was set")
	}
	if cookie.HttpOnly {
		t.Error("cookie is HttpOnly; the dashboard cannot read the date to skip its daily check")
	}
	if cookie.Path != "/" || cookie.MaxAge != versioncheck.CookieMaxAge {
		t.Errorf("cookie path/max-age = %q/%d", cookie.Path, cookie.MaxAge)
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}
	// httptest requests are plain HTTP; Secure would make the browser drop
	// the cookie and turn the daily gate into a per-page-load check.
	if cookie.Secure {
		t.Error("Secure was set on a plain-HTTP request")
	}
}

func TestVersionEndpointSurvivesAnUnreachableManifest(t *testing.T) {
	srv, _, _ := versionTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want the local version served anyway", rec.Code)
	}
	status := decodeStatus(t, rec)
	if status.Version != "0.1.81" || status.UpdateAvailable {
		t.Fatalf("got %+v, want the local version with no update claimed", status)
	}
}

func TestVersionEndpointWithoutACheckerReportsLocalBuild(t *testing.T) {
	srv := New(&mockProvider{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	status := decodeStatus(t, rec)
	if status.App == "" || status.Version == "" {
		t.Fatalf("got %+v, want the local build reported", status)
	}
	if status.UpdateAvailable {
		t.Error("an unchecked gateway must not claim an update is available")
	}
}

func TestVersionEndpointSkipsAuthentication(t *testing.T) {
	srv := New(&mockProvider{}, &Config{MasterKey: "sk-master-key"})

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want /version reachable without credentials", rec.Code)
	}
}
