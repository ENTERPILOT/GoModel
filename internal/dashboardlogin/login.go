// Package dashboardlogin implements the dashboard's username and password
// login. A successful sign-in starts a browser session held in a signed
// cookie, which the authentication middleware accepts in place of a bearer
// credential.
//
// Sessions are stateless: the cookie carries its own expiry, as
// "<unix seconds>.<MAC>", and the MAC is keyed from the configured username and
// password. Every replica started with the
// same credentials therefore accepts the same sessions without shared storage,
// and changing either credential ends every session.
package dashboardlogin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/enterpilot/gomodel/ext"
)

const (
	// CookieName names the session cookie.
	CookieName = "gomodel_dashboard_session"
	// SessionTTL is how long a sign-in lasts. A session cannot be revoked
	// before then short of changing the credentials, so it stays short.
	SessionTTL = 24 * time.Hour
	// AuthMethod is the authentication method recorded in the request log.
	AuthMethod = "dashboard_login"

	// The session key is stretched from the password, so a captured cookie
	// cannot be used to guess the password offline at the speed of a plain
	// hash. A sign-in is checked by stretching the submitted password the
	// same way. Parameters follow the OWASP Argon2id minimum.
	keyTime    = 2
	keyMemory  = 19 * 1024 // KiB
	keyThreads = 1
	keyLength  = 32
	keySalt    = "gomodel/dashboard-session/v1\x00"

	// maxPendingChecks bounds the sign-ins waiting for their password to be
	// checked. Checks run one at a time, so a longer line would only hold
	// requests open behind a flood; past it a sign-in is told to retry.
	maxPendingChecks = 4
)

// errBusy reports that too many sign-ins are waiting to be checked.
var errBusy = errors.New("too many sign-ins in progress")

// Service verifies dashboard credentials and the sessions issued for them. It
// is safe for concurrent use.
type Service struct {
	username string
	key      []byte
	// deriving admits one key derivation at a time: each takes keyMemory,
	// and sign-ins are rare. checks holds a slot per sign-in waiting for it.
	deriving sync.Mutex
	checks   chan struct{}
	attempts *attemptLimiter
	recorder atomic.Pointer[ext.AuthenticationEventRecorder]
	now      func() time.Time
}

// New creates the login service for one username and password.
func New(username, password string) (*Service, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("dashboard login needs a username")
	}
	if password == "" {
		return nil, errors.New("dashboard login needs a password")
	}
	return &Service{
		username: username,
		key:      deriveKey(username, password),
		checks:   make(chan struct{}, maxPendingChecks),
		attempts: newAttemptLimiter(),
		now:      time.Now,
	}, nil
}

func deriveKey(username, password string) []byte {
	return argon2.IDKey([]byte(password), []byte(keySalt+username), keyTime, keyMemory, keyThreads, keyLength)
}

// Username returns the configured username.
func (s *Service) Username() string {
	return s.username
}

// Name implements ext.RequestAuthenticator.
func (s *Service) Name() string {
	return AuthMethod
}

// SetAuthenticationEventRecorder implements
// ext.AuthenticationEventRecorderAware, so sign-ins and sign-outs reach the
// request log.
func (s *Service) SetAuthenticationEventRecorder(recorder ext.AuthenticationEventRecorder) {
	if recorder == nil {
		s.recorder.Store(nil)
		return
	}
	s.recorder.Store(&recorder)
}

// AuthenticateRequest implements ext.RequestAuthenticator: it accepts a
// request carrying a valid session cookie. A request without one, or one a
// browser sent on behalf of another site, is not this mechanism's to answer.
func (s *Service) AuthenticateRequest(_ context.Context, request *http.Request) (*ext.Authentication, error) {
	if _, ok := s.session(request); !ok || !sameOrigin(request) {
		return nil, nil
	}
	return &ext.Authentication{
		PrincipalID:     s.username,
		DashboardAccess: true,
		Method:          AuthMethod,
	}, nil
}

// session returns the expiry of the request's session cookie when it holds a
// valid, unexpired session.
func (s *Service) session(request *http.Request) (time.Time, bool) {
	cookie, err := request.Cookie(CookieName)
	if err != nil {
		return time.Time{}, false
	}
	return s.verify(cookie.Value)
}

// credentialsMatch reports whether the username and password are the
// configured ones. The password is never compared directly: it is stretched
// into the key the configured password produced, and the keys are compared in
// constant time. Both values are always checked, so a wrong username answers
// no faster than a wrong password. It returns errBusy, without checking, when
// maxPendingChecks sign-ins are already waiting.
func (s *Service) credentialsMatch(username, password string) (bool, error) {
	select {
	case s.checks <- struct{}{}:
		defer func() { <-s.checks }()
	default:
		return false, errBusy
	}
	usernameOK := subtle.ConstantTimeCompare([]byte(strings.TrimSpace(username)), []byte(s.username))
	s.deriving.Lock()
	key := deriveKey(s.username, password)
	s.deriving.Unlock()
	return usernameOK&subtle.ConstantTimeCompare(key, s.key) == 1, nil
}

// issue returns a session token and the time it expires.
func (s *Service) issue() (string, time.Time) {
	expires := s.now().Add(SessionTTL).Truncate(time.Second)
	expiry := strconv.FormatInt(expires.Unix(), 10)
	return expiry + "." + base64.RawURLEncoding.EncodeToString(s.mac(expiry)), expires
}

// verify returns the expiry of token when this service issued it and it has
// not expired.
func (s *Service) verify(token string) (time.Time, bool) {
	expiry, encodedMAC, found := strings.Cut(token, ".")
	if !found {
		return time.Time{}, false
	}
	mac, err := base64.RawURLEncoding.DecodeString(encodedMAC)
	if err != nil || !hmac.Equal(mac, s.mac(expiry)) {
		return time.Time{}, false
	}
	// The MAC covers the expiry exactly as written, so it parses as issued.
	seconds, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	expires := time.Unix(seconds, 0)
	if !s.now().Before(expires) {
		return time.Time{}, false
	}
	return expires, true
}

func (s *Service) mac(expiry string) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(expiry))
	return mac.Sum(nil)
}

// sameOrigin reports whether a browser sent the request from the gateway's own
// pages, or no browser was involved. The session cookie is ambient, so it must
// not authorize a request another site made the browser send. SameSite keeps
// the cookie off cross-site requests; this also covers a sibling subdomain,
// which is same-site. Sec-Fetch-Site is a forbidden header name, so page
// script can neither set nor remove it.
func sameOrigin(request *http.Request) bool {
	switch request.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
		// Not a current browser: fall back to the Origin it may have sent.
		origin := request.Header.Get("Origin")
		if origin == "" {
			return true
		}
		parsed, err := url.Parse(origin)
		return err == nil && strings.EqualFold(parsed.Host, request.Host)
	default:
		return false
	}
}
