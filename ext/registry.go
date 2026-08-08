package ext

import (
	"slices"
	"sync"

	"github.com/labstack/echo/v5"
)

// Registry collects extensions to be consumed by the gateway at startup.
// Register everything before the server is constructed (before run.Run or
// app.New); core snapshots each registration list during initialization.
type Registry struct {
	mu              sync.Mutex
	rewriters       []RequestRewriter
	outerMiddleware []echo.MiddlewareFunc
	middleware      []echo.MiddlewareFunc
	routes          []func(*echo.Echo)
	publicPaths     []string
	routeSelector   RouteSelector
	settings        []RuntimeSetting
	authenticators  []RequestAuthenticator
	observers       []UpstreamObserver
}

// UseOuterMiddleware adds middleware at the outer HTTP boundary, after
// credential-like request URI values are redacted and before request logging,
// recovery, limits, audit capture, and authentication. It is intended for
// observability and correlation middleware that must cover the whole request.
// It must not depend on an authenticated identity.
func (r *Registry) UseOuterMiddleware(m echo.MiddlewareFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outerMiddleware = append(r.outerMiddleware, m)
}

// RegisterUpstreamObserver adds an observer for logical provider calls.
// Observers run in registration order and may derive the context passed to
// later observers and to the provider request.
func (r *Registry) RegisterUpstreamObserver(observer UpstreamObserver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.observers = append(r.observers, observer)
}

// RegisterAuthenticator adds a request authentication mechanism. Core bearer
// tokens keep precedence when a request explicitly supplies one.
func (r *Registry) RegisterAuthenticator(authenticator RequestAuthenticator) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.authenticators = append(r.authenticators, authenticator)
}

// RegisterSetting adds a deployment-wide setting exposed through the generic
// admin settings API.
func (r *Registry) RegisterSetting(setting RuntimeSetting) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settings = append(r.settings, setting)
}

// RegisterRewriter adds a request rewriter. Rewriters run in registration
// order, each receiving the previous rewriter's output.
func (r *Registry) RegisterRewriter(rw RequestRewriter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rewriters = append(r.rewriters, rw)
}

// UseMiddleware adds an Echo middleware that runs after audit capture and
// before gateway authentication, so it can normalize credentials (for
// example an SSO session) before the gateway auth check.
func (r *Registry) UseMiddleware(m echo.MiddlewareFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.middleware = append(r.middleware, m)
}

// RegisterRoutes adds a callback that registers extra routes after all core
// routes. Paths are relative to the server base path.
func (r *Registry) RegisterRoutes(fn func(e *echo.Echo)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = append(r.routes, fn)
}

// AddPublicPaths appends paths to the authentication skip list (for example
// OAuth callback endpoints). A trailing "/*" matches a prefix.
func (r *Registry) AddPublicPaths(paths ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publicPaths = append(r.publicPaths, paths...)
}

// RegisterRouteSelector installs the route selector consulted by virtual
// models using the "adaptive" load-balancing strategy. Only one selector can
// be active; a later registration replaces an earlier one.
func (r *Registry) RegisterRouteSelector(sel RouteSelector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routeSelector = sel
}

// Rewriters returns a defensive copy of the registered rewriters.
func (r *Registry) Rewriters() []RequestRewriter {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.rewriters)
}

// Middleware returns a defensive copy of the registered middleware.
func (r *Registry) Middleware() []echo.MiddlewareFunc {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.middleware)
}

// OuterMiddleware returns a defensive copy of registered outer middleware.
func (r *Registry) OuterMiddleware() []echo.MiddlewareFunc {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.outerMiddleware)
}

// Routes returns a defensive copy of the registered route callbacks.
func (r *Registry) Routes() []func(*echo.Echo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.routes)
}

// PublicPaths returns a defensive copy of the registered public paths.
func (r *Registry) PublicPaths() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.publicPaths)
}

// RouteSelector returns the registered route selector, or nil.
func (r *Registry) RouteSelector() RouteSelector {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.routeSelector
}

// Settings returns a defensive copy of the registered runtime settings.
func (r *Registry) Settings() []RuntimeSetting {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.settings)
}

// Authenticators returns a defensive copy of registered request authenticators.
func (r *Registry) Authenticators() []RequestAuthenticator {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.authenticators)
}

// UpstreamObservers returns a defensive copy of registered observers.
func (r *Registry) UpstreamObservers() []UpstreamObserver {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.observers)
}

// Default is the process-wide registry used by package-level helpers and, by
// default, by run.Run.
var Default = &Registry{}

// RegisterRewriter registers a rewriter on the Default registry.
func RegisterRewriter(rw RequestRewriter) { Default.RegisterRewriter(rw) }

// UseMiddleware registers middleware on the Default registry.
func UseMiddleware(m echo.MiddlewareFunc) { Default.UseMiddleware(m) }

// UseOuterMiddleware registers outer HTTP middleware on the Default registry.
func UseOuterMiddleware(m echo.MiddlewareFunc) { Default.UseOuterMiddleware(m) }

// RegisterRoutes registers a route callback on the Default registry.
func RegisterRoutes(fn func(e *echo.Echo)) { Default.RegisterRoutes(fn) }

// AddPublicPaths registers auth-skip paths on the Default registry.
func AddPublicPaths(paths ...string) { Default.AddPublicPaths(paths...) }

// RegisterRouteSelector installs a route selector on the Default registry.
func RegisterRouteSelector(sel RouteSelector) { Default.RegisterRouteSelector(sel) }

// RegisterSetting registers a runtime setting on the Default registry.
func RegisterSetting(setting RuntimeSetting) { Default.RegisterSetting(setting) }

// RegisterAuthenticator registers a request authenticator on the Default registry.
func RegisterAuthenticator(authenticator RequestAuthenticator) {
	Default.RegisterAuthenticator(authenticator)
}

// RegisterUpstreamObserver registers an observer on the Default registry.
func RegisterUpstreamObserver(observer UpstreamObserver) {
	Default.RegisterUpstreamObserver(observer)
}
