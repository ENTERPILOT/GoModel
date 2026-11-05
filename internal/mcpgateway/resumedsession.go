package mcpgateway

import (
	"fmt"
	"mime"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resumedSession hosts a session this replica rebuilt from the shared store.
// The SDK handler only serves sessions it created, so this serves the
// session's HTTP exchanges itself, through the session's own transport, with
// the handler's request checks and idle timeout.
type resumedSession struct {
	session   *mcp.ServerSession
	transport *mcp.StreamableServerTransport

	mu sync.Mutex
	// posts counts in-flight POSTs, which pause the idle timer like the SDK
	// handler's; timer is nil once stopped.
	posts int
	timer *time.Timer
}

func newResumedSession(session *mcp.ServerSession, transport *mcp.StreamableServerTransport) *resumedSession {
	host := &resumedSession{session: session, transport: transport}
	host.timer = time.AfterFunc(sessionIdleTimeout, host.close)
	return host
}

// ServeHTTP mirrors the SDK handler's stateful GET, POST, and DELETE. Its
// loopback Host check is left out: originGuard has already run on every bind
// address.
func (h *resumedSession) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Before the method switch, as in the SDK handler: a DELETE with an
	// unsupported version is rejected, not run.
	if rejectUnsupportedProtocolVersion(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		if _, streamOK := streamableAccepts(r.Header.Values("Accept")); !streamOK {
			http.Error(w, "Accept must contain 'text/event-stream' for GET requests", http.StatusBadRequest)
			return
		}
	case http.MethodPost:
		if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
			http.Error(w, "Content-Type must be 'application/json'", http.StatusUnsupportedMediaType)
			return
		}
		if jsonOK, streamOK := streamableAccepts(r.Header.Values("Accept")); !jsonOK || !streamOK {
			http.Error(w, "Accept must contain both 'application/json' and 'text/event-stream'", http.StatusBadRequest)
			return
		}
	case http.MethodDelete:
		h.close()
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, mcp.DefaultMaxRequestBodyBytes)
	}
	if r.Method == http.MethodPost {
		h.startPOST()
		defer h.endPOST()
	}
	h.transport.ServeHTTP(w, r)
}

func (h *resumedSession) startPOST() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.timer != nil && h.posts == 0 {
		h.timer.Stop()
	}
	h.posts++
}

func (h *resumedSession) endPOST() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.posts--
	if h.timer != nil && h.posts == 0 {
		h.timer.Reset(sessionIdleTimeout)
	}
}

func (h *resumedSession) stopTimer() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
}

// close ends the session; it waits for in-flight requests to drain.
func (h *resumedSession) close() {
	h.stopTimer()
	_ = h.session.Close()
}

// sessionProtocolVersions are the protocol versions every session server
// negotiates, and the ones rejectUnsupportedProtocolVersion accepts.
var sessionProtocolVersions = mcp.SupportedProtocolVersions()

// perRequestProtocolVersion is the first protocol version that carries its
// version on every request (SEP-2575). The SDK handler passes a version from
// it on that it does not list to its own handling instead of rejecting it.
const perRequestProtocolVersion = "2026-07-28"

// rejectUnsupportedProtocolVersion answers 400 to a request whose
// Mcp-Protocol-Version the session servers do not negotiate, matching the SDK
// handler's check. It runs before a request touches its session, so a
// rejected DELETE leaves the session open.
func rejectUnsupportedProtocolVersion(w http.ResponseWriter, r *http.Request) bool {
	version := r.Header.Get("Mcp-Protocol-Version")
	if version == "" || slices.Contains(sessionProtocolVersions, version) || version >= perRequestProtocolVersion {
		return false
	}
	http.Error(w, fmt.Sprintf("Bad Request: Unsupported protocol version (supported versions: %s)", strings.Join(sessionProtocolVersions, ",")), http.StatusBadRequest)
	return true
}

// streamableAccepts reports whether Accept admits JSON and an event stream,
// matching the SDK handler's check.
func streamableAccepts(values []string) (jsonOK, streamOK bool) {
	for _, value := range values {
		for token := range strings.SplitSeq(value, ",") {
			base, _, _ := strings.Cut(token, ";")
			switch strings.ToLower(strings.TrimSpace(base)) {
			case "application/json", "application/*":
				jsonOK = true
			case "text/event-stream", "text/*":
				streamOK = true
			case "*/*":
				jsonOK, streamOK = true, true
			}
		}
	}
	return jsonOK, streamOK
}
