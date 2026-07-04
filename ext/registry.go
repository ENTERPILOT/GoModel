package ext

import (
	"slices"
	"sync"

	"github.com/labstack/echo/v5"
)

// Registry collects extensions to be consumed by the gateway at startup.
// Register everything before the server is constructed (before run.Run or
// app.New); core snapshots the registry once and never consults it again.
type Registry struct {
	mu          sync.Mutex
	rewriters   []RequestRewriter
	middleware  []echo.MiddlewareFunc
	routes      []func(*echo.Echo)
	publicPaths []string
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

// Default is the process-wide registry used by package-level helpers and, by
// default, by run.Run.
var Default = &Registry{}

// RegisterRewriter registers a rewriter on the Default registry.
func RegisterRewriter(rw RequestRewriter) { Default.RegisterRewriter(rw) }

// UseMiddleware registers middleware on the Default registry.
func UseMiddleware(m echo.MiddlewareFunc) { Default.UseMiddleware(m) }

// RegisterRoutes registers a route callback on the Default registry.
func RegisterRoutes(fn func(e *echo.Echo)) { Default.RegisterRoutes(fn) }

// AddPublicPaths registers auth-skip paths on the Default registry.
func AddPublicPaths(paths ...string) { Default.AddPublicPaths(paths...) }
