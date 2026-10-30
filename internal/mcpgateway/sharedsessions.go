package mcpgateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Downstream sessions are shared between replicas through the shared store.
// The replica that opens a session stores its binding (principal, endpoint,
// scope, and the client's initialize parameters) once initialize succeeds.
// A replica that gets a request for a session it does not host reads that
// record, checks the request against it, and rebuilds the session from it.
// Closing a session writes a close mark the other replicas check, so a
// session ends everywhere, not only where the DELETE landed.
const sharedSessionNamespace = "mcp.sessions"

const (
	// sessionRecheckInterval bounds how long a replica keeps serving a session
	// another replica closed: it rereads the close marks at most this often.
	sessionRecheckInterval = 2 * time.Second
	// sessionTouchInterval bounds how often a replica extends a session's
	// shared expiry, so an active session costs one write a minute, not one
	// per request.
	sessionTouchInterval = time.Minute
	// sharedSessionTimeout bounds each shared-store call on the request path.
	sharedSessionTimeout = 2 * time.Second
	// sessionCloseMarkLifetime keeps close marks past the last expiry a
	// record they shadow can still have.
	sessionCloseMarkLifetime = 2 * sessionIdleTimeout
)

// sharedSession is the shared-store record of one session. It holds the
// binding and what the SDK needs to restore the session, never a secret.
type sharedSession struct {
	AuthKeyID string `json:"auth_key_id,omitempty"`
	UserPath  string `json:"user_path,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	// Scoped marks a session opened with ScopeHeader; Servers is its set.
	Scoped    bool     `json:"scoped,omitempty"`
	Servers   []string `json:"servers,omitempty"`
	Discovery bool     `json:"discovery,omitempty"`
	// OpenedAt is in unix nanoseconds.
	OpenedAt        int64                 `json:"opened_at"`
	Initialize      *mcp.InitializeParams `json:"initialize"`
	ProtocolVersion string                `json:"protocol_version"`
}

// scope rebuilds the visibility inputs the session was opened with: later
// requests never change a session's view, wherever they land.
func (r sharedSession) scope() requestScope {
	scope := requestScope{authKeyID: r.AuthKeyID, userPath: r.UserPath, discovery: r.Discovery}
	if virtual, ok := strings.CutPrefix(r.Endpoint, virtualBindingPrefix); ok {
		scope.virtual = virtual
	} else {
		scope.pinned = r.Endpoint
	}
	if r.Scoped {
		scope.include = make(map[string]struct{}, len(r.Servers))
		for _, name := range r.Servers {
			scope.include[name] = struct{}{}
		}
	}
	return scope
}

// Keys hold a digest of the session ID, never the ID: it is a bearer token
// for the session, and the shared store is readable by every replica.
func sessionDigest(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(sum[:])
}

func sessionKey(sessionID string) string { return "session." + sessionDigest(sessionID) }

func sessionCloseKey(sessionID string) string { return "closed." + sessionDigest(sessionID) }

func endpointCloseKey(endpoint string) string { return "endpoint." + endpoint }

type sessionAdmission int

const (
	// sessionAdmitted passes the request to the session's host: the resumed
	// session admitSession returns, or else the SDK handler, which answers
	// 404 for an ID it does not know.
	sessionAdmitted sessionAdmission = iota
	// sessionRejected answers 404.
	sessionRejected
	// sessionEnded answers 204: a DELETE closed a session no replica-local
	// host had to close.
	sessionEnded
)

// admitSession checks a request's session ID against the binding the session
// was opened under, on this replica or in the shared store, and finds its
// host. remove marks a DELETE, which ends the session on every replica.
func (s *Service) admitSession(ctx context.Context, sessionID, authKeyID, userPath, endpoint string, remove bool) (sessionAdmission, *resumedSession) {
	binding, ok := s.liveBinding(sessionID)
	if !ok {
		if s.shared == nil {
			// Unknown session IDs pass through: the SDK rejects them itself,
			// and bindings do not survive restarts.
			return sessionAdmitted, nil
		}
		return s.admitSharedSession(ctx, sessionID, authKeyID, userPath, endpoint, remove)
	}
	if !binding.boundTo(authKeyID, userPath, endpoint) {
		return sessionRejected, nil
	}
	if remove {
		s.unbind(sessionID, binding.server)
		if binding.shared {
			s.closeSharedSession(ctx, sessionID)
		}
		return sessionAdmitted, binding.resumed
	}
	if binding.shared && !s.recheckSharedSession(ctx, sessionID, binding) {
		s.unbind(sessionID, binding.server)
		closeSessions(binding.server)
		return sessionRejected, nil
	}
	return sessionAdmitted, binding.resumed
}

// admitSharedSession admits a request for a session this replica does not
// host by its shared record, resuming the session here.
func (s *Service) admitSharedSession(ctx context.Context, sessionID, authKeyID, userPath, endpoint string, remove bool) (sessionAdmission, *resumedSession) {
	lookupCtx, cancel := context.WithTimeout(ctx, sharedSessionTimeout)
	record, raw, err := s.loadSharedSession(lookupCtx, sessionID)
	cancel()
	if err != nil {
		// Unverifiable is unknown: the client re-initializes.
		slog.Warn("failed to read mcp session from the shared store", "error", err)
		return sessionRejected, nil
	}
	if record == nil {
		return sessionAdmitted, nil
	}
	if record.AuthKeyID != authKeyID || record.UserPath != userPath || record.Endpoint != endpoint {
		return sessionRejected, nil
	}
	if remove {
		s.closeSharedSession(ctx, sessionID)
		return sessionEnded, nil
	}
	host, err, _ := s.resumes.Do(sessionID, func() (any, error) {
		return s.resumeSession(ctx, sessionID, record, raw)
	})
	if err != nil {
		slog.Warn("failed to resume mcp session from the shared store", "error", err)
		return sessionRejected, nil
	}
	return sessionAdmitted, host.(*resumedSession)
}

// loadSharedSession returns the shared record of a session that is neither
// closed nor expired, or nil.
func (s *Service) loadSharedSession(ctx context.Context, sessionID string) (*sharedSession, []byte, error) {
	raw, found, err := s.shared.Get(ctx, sharedSessionNamespace, sessionKey(sessionID))
	if err != nil || !found {
		return nil, nil, err
	}
	var record sharedSession
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, nil, fmt.Errorf("decode shared mcp session: %w", err)
	}
	if record.Initialize == nil {
		return nil, nil, fmt.Errorf("shared mcp session has no initialize parameters")
	}
	closed, err := s.sharedSessionClosed(ctx, sessionID, record.Endpoint, time.Unix(0, record.OpenedAt))
	if err != nil || closed {
		return nil, nil, err
	}
	return &record, raw, nil
}

// sharedSessionClosed reports whether a session was closed on any replica:
// by a DELETE, or by deleting the virtual server it was opened on. The
// second mark outlives the endpoint, so a virtual server recreated under the
// same name does not revive sessions of the deleted one.
func (s *Service) sharedSessionClosed(ctx context.Context, sessionID, endpoint string, openedAt time.Time) (bool, error) {
	if _, closed, err := s.shared.Get(ctx, sharedSessionNamespace, sessionCloseKey(sessionID)); err != nil || closed {
		return closed, err
	}
	if !strings.HasPrefix(endpoint, virtualBindingPrefix) {
		return false, nil
	}
	value, found, err := s.shared.Get(ctx, sharedSessionNamespace, endpointCloseKey(endpoint))
	if err != nil || !found {
		return false, err
	}
	closedAt, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil {
		return false, fmt.Errorf("decode mcp endpoint close mark: %w", err)
	}
	return !openedAt.After(time.Unix(0, closedAt)), nil
}

// recheckSharedSession reports whether a hosted session is still open
// cluster-wide, and extends its shared expiry. Both run at a bounded rate, so
// most requests touch no database. A store error keeps the session: its
// binding is still checked locally on every request.
func (s *Service) recheckSharedSession(ctx context.Context, sessionID string, binding sessionBinding) bool {
	now := s.now()
	if now.Sub(binding.checkedAt) < sessionRecheckInterval {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, sharedSessionTimeout)
	defer cancel()
	closed, err := s.sharedSessionClosed(ctx, sessionID, binding.endpoint, binding.openedAt)
	if err != nil {
		slog.Warn("failed to recheck mcp session in the shared store", "error", err)
	} else if closed {
		return false
	}
	touch := now.Sub(binding.touchedAt) >= sessionTouchInterval
	if touch {
		// A touch racing a DELETE elsewhere may rewrite a deleted record;
		// the close mark still ends the session, since every read checks it.
		if err := s.shared.Put(ctx, sharedSessionNamespace, sessionKey(sessionID), binding.record, now.Add(sessionIdleTimeout)); err != nil {
			slog.Warn("failed to extend mcp session in the shared store", "error", err)
		}
	}
	s.bindMu.Lock()
	if current, ok := s.bindings[sessionID]; ok && current.server == binding.server {
		current.checkedAt = now
		if touch {
			current.touchedAt = now
		}
		s.bindings[sessionID] = current
	}
	s.bindMu.Unlock()
	return true
}

// resumeSession rebuilds a session from its shared record and hosts it here.
// The SDK restores the negotiated state it carries, so the session continues
// without a new initialize.
func (s *Service) resumeSession(ctx context.Context, sessionID string, record *sharedSession, raw []byte) (*resumedSession, error) {
	s.bindMu.Lock()
	binding, ok := s.bindings[sessionID]
	s.bindMu.Unlock()
	if ok && binding.resumed != nil {
		return binding.resumed, nil // resumed by a request that overlapped this one
	}

	scope := record.scope()
	server, aliases := s.newSessionServer(scope)
	transport := &mcp.StreamableServerTransport{SessionID: sessionID}
	session, err := server.Connect(context.WithoutCancel(ctx), transport, &mcp.ServerSessionOptions{
		State: &mcp.ServerSessionState{
			InitializeParams:          record.Initialize,
			InitializedParams:         &mcp.InitializedParams{},
			NegotiatedProtocolVersion: record.ProtocolVersion,
		},
	})
	if err != nil {
		return nil, err
	}
	host := newResumedSession(session, transport)

	// Resuming is activity: extend the expiry now rather than a minute later.
	now := s.now()
	putCtx, cancel := context.WithTimeout(ctx, sharedSessionTimeout)
	if err := s.shared.Put(putCtx, sharedSessionNamespace, sessionKey(sessionID), raw, now.Add(sessionIdleTimeout)); err != nil {
		slog.Warn("failed to extend mcp session in the shared store", "error", err)
	}
	cancel()

	s.bindMu.Lock()
	// Checked under bindMu, which closeResumedSessions takes after stopCtx is
	// cancelled: either Close sees this binding or this sees Close.
	if s.stopCtx.Err() != nil {
		s.bindMu.Unlock()
		host.close()
		return nil, errServiceClosed
	}
	s.bindings[sessionID] = sessionBinding{
		authKeyID:   record.AuthKeyID,
		userPath:    record.UserPath,
		endpoint:    record.Endpoint,
		lastSeen:    now,
		openedAt:    time.Unix(0, record.OpenedAt),
		discovery:   scope.discovery,
		toolAliases: aliases,
		server:      server,
		resumed:     host,
		shared:      true,
		record:      raw,
		checkedAt:   now,
		touchedAt:   now,
	}
	s.bindMu.Unlock()
	go s.awaitResumed(sessionID, host)
	return host, nil
}

// awaitResumed drops a resumed session's binding once the session ends,
// however it ends: idle timeout, DELETE, or an endpoint closing.
func (s *Service) awaitResumed(sessionID string, host *resumedSession) {
	_ = host.session.Wait()
	host.stopTimer()
	s.bindMu.Lock()
	if binding, ok := s.bindings[sessionID]; ok && binding.resumed == host {
		delete(s.bindings, sessionID)
	}
	s.bindMu.Unlock()
}

// closeResumedSessions ends the sessions this replica resumed, at Close. The
// replicas that remain resume them again on the client's next request.
func (s *Service) closeResumedSessions() {
	s.bindMu.Lock()
	var hosts []*resumedSession
	for _, binding := range s.bindings {
		if binding.resumed != nil {
			hosts = append(hosts, binding.resumed)
		}
	}
	s.bindMu.Unlock()
	for _, host := range hosts {
		go host.close()
	}
}

// shareOnInitialize stores a new session's record once its initialize
// succeeds, before the response carries the session ID to the client, so
// any replica can resume it from the client's next request on.
func (s *Service) shareOnInitialize(scope requestScope) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if err != nil || method != "initialize" {
				return result, err
			}
			initialize, ok := req.(*mcp.ServerRequest[*mcp.InitializeParams])
			initialized, resultOK := result.(*mcp.InitializeResult)
			if ok && resultOK && initialize.Session != nil {
				s.shareSession(ctx, initialize.Session.ID(), scope, initialize.Params, initialized.ProtocolVersion)
			}
			return result, err
		}
	}
}

func (s *Service) shareSession(ctx context.Context, sessionID string, scope requestScope, params *mcp.InitializeParams, protocolVersion string) {
	s.bindMu.Lock()
	binding, ok := s.bindings[sessionID]
	s.bindMu.Unlock()
	if !ok || params == nil {
		return
	}
	record := sharedSession{
		AuthKeyID:       binding.authKeyID,
		UserPath:        binding.userPath,
		Endpoint:        binding.endpoint,
		Discovery:       binding.discovery,
		OpenedAt:        binding.openedAt.UnixNano(),
		Initialize:      params,
		ProtocolVersion: protocolVersion,
	}
	if scope.include != nil {
		record.Scoped = true
		record.Servers = slices.Sorted(maps.Keys(scope.include))
	}
	raw, err := json.Marshal(record)
	if err != nil {
		slog.Warn("failed to encode mcp session for the shared store; it is served by this replica only", "error", err)
		return
	}
	now := s.now()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sharedSessionTimeout)
	defer cancel()
	if err := s.shared.Put(ctx, sharedSessionNamespace, sessionKey(sessionID), raw, now.Add(sessionIdleTimeout)); err != nil {
		slog.Warn("failed to share mcp session; it is served by this replica only", "error", err)
		return
	}
	s.bindMu.Lock()
	if current, ok := s.bindings[sessionID]; ok && current.server == binding.server {
		current.shared = true
		current.record = raw
		current.checkedAt = now
		current.touchedAt = now
		s.bindings[sessionID] = current
	}
	s.bindMu.Unlock()
}

// closeSharedSession ends a session on every replica: the close mark stops
// the ones hosting it, and deleting the record stops new resumes.
func (s *Service) closeSharedSession(ctx context.Context, sessionID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sharedSessionTimeout)
	defer cancel()
	if err := s.shared.Put(ctx, sharedSessionNamespace, sessionCloseKey(sessionID), nil, s.now().Add(sessionCloseMarkLifetime)); err != nil {
		slog.Warn("failed to close mcp session in the shared store", "error", err)
	}
	if err := s.shared.Delete(ctx, sharedSessionNamespace, sessionKey(sessionID)); err != nil {
		slog.Warn("failed to delete mcp session from the shared store", "error", err)
	}
}

// closeSharedEndpoint ends, on every replica, the sessions opened on endpoint
// up to now; closeEndpointSessions ends the ones this replica hosts.
func (s *Service) closeSharedEndpoint(ctx context.Context, endpoint string) {
	if s.shared == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sharedSessionTimeout)
	defer cancel()
	now := s.now()
	mark := []byte(strconv.FormatInt(now.UnixNano(), 10))
	if err := s.shared.Put(ctx, sharedSessionNamespace, endpointCloseKey(endpoint), mark, now.Add(sessionCloseMarkLifetime)); err != nil {
		slog.Warn("failed to close mcp endpoint sessions in the shared store", "endpoint", endpoint, "error", err)
	}
}

// unbind drops a session's binding unless a newer host took its ID.
func (s *Service) unbind(sessionID string, server *mcp.Server) {
	s.bindMu.Lock()
	if binding, ok := s.bindings[sessionID]; ok && binding.server == server {
		delete(s.bindings, sessionID)
	}
	s.bindMu.Unlock()
}

// closeSessions ends the sessions server hosts. Closing waits for in-flight
// requests to drain, so it runs in the background.
func closeSessions(server *mcp.Server) {
	if server == nil {
		return
	}
	for session := range server.Sessions() {
		go func() { _ = session.Close() }()
	}
}
