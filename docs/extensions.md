# Building on GoModel: the `ext` and `run` packages

GoModel can be embedded as a Go module to build custom gateway binaries —
distributions that add request rewriting, extra HTTP endpoints, or custom
middleware on top of the full gateway. The open-source binary is itself a
thin wrapper over the same API.

> **Consuming from another module:** depend on GoModel like any Go module:
> `require github.com/ENTERPILOT/GoModel vX.Y.Z`.
>
> **API stability:** the `ext` and `run` packages are experimental — no
> compatibility promise until they are declared stable.

```go
package main

import (
	"context"
	"os"

	"github.com/ENTERPILOT/GoModel/ext"
	"github.com/ENTERPILOT/GoModel/run"
)

func main() {
	err := run.Run(context.Background(), run.Options{
		ProductName: "my-gateway",
		Setup: func(context.Context) error {
			ext.RegisterRewriter(&myRewriter{})
			return nil
		},
	})
	if code := run.ExitCode(err); code != 0 {
		os.Exit(code)
	}
}
```

## `run.Run`

`run.Run(ctx, run.Options)` executes the complete gateway lifecycle: CLI
parsing (`--version`, `--health`, `--ready`), dotenv loading, logging setup,
`config.Load()`, registration of all built-in providers, application
construction, signal handling, and graceful shutdown. Cancelling `ctx`
triggers the same graceful shutdown as SIGINT/SIGTERM. `run.ExitCode` maps
the returned error to a process exit code (usage errors → 2, failures → 1).

`Options`:

- `ProductName` — used in CLI usage output, the startup log, and `--version`.
- `Extensions` — the `*ext.Registry` to consume (default `ext.Default`).
- `Setup func(ctx) error` — runs only when the process is committed to
  starting the gateway (never for `--version` or probe modes). Register
  extensions and validate product licensing here so operator tooling stays
  silent. A returned error aborts startup.
- `Args`, `Stdout`, `Stderr` — overridable for embedding and tests.
- `ConfigureSwaggerDocs func(basePath string)` — hook for a generated
  swagger docs package (the core binary passes its build-tagged one).

## `ext`: the extension registry

Register everything before the server is constructed; core snapshots the
registry once. An empty registry adds zero request overhead.

### Request rewriters

```go
type RequestRewriter interface {
	Name() string
	Rewrite(ctx context.Context, in Input) (*Result, error)
}
```

Rewriters receive the **raw JSON body** of `POST /v1/chat/completions`,
`/v1/messages`, and `/v1/responses` (subroutes excluded) and may return a
rewritten body plus response-header annotations. They run:

- **after authentication** — only authenticated traffic, final user path;
- **before workflow resolution** — body changes (including `"model"`)
  affect routing, failover, guardrails, budgets, and caching;
- in registration order, each receiving the previous rewriter's output;
- **fail-closed** — returning an error rejects the request. Use
  `*ext.RejectionError{Status, Code, Message}` for a client-visible
  rejection rendered in the endpoint's native error dialect; any other error
  maps to HTTP 500. Core logs only the rewriter name and error, never
  bodies.

Contract notes: treat `Input.Body` as read-only and return a new slice;
implementations must be safe for concurrent use. **Audit logs always record
the original client body**, not the rewritten one — what rewriters did is
tracked separately:

- Every body change appends a `request_revisions` entry to the audit
  entry (`RequestRevisionSnapshot`): sequence number, rewriter name, body
  sizes before/after, the rewritten body (only when body logging is
  enabled and within the capture limit), and the rewriter's `Result.Detail`.
  The original request plus the revision chain reconstruct the full
  transformation history; the last revision is what core parsed and
  forwarded.
- `Result.Detail` may carry any JSON-serializable summary of the change —
  it lands in the revision entry and is never sent upstream.
- `Result.ResponseHeader` annotates the HTTP response for clients.

### Middleware, routes, and public paths

- `UseMiddleware(m echo.MiddlewareFunc)` — runs after audit capture and
  before gateway authentication, so an SSO/session middleware can normalize
  credentials for the auth check.
- `RegisterRoutes(func(e *echo.Echo))` — called after all core routes.
- `AddPublicPaths(paths ...string)` — appends to the auth skip list
  (`"/prefix/*"` matches a prefix); use for OAuth callbacks and similar.

## Practicalities

- **Versioning**: `internal/version` variables are plain vars — set them
  from your build with
  `-ldflags '-X github.com/ENTERPILOT/GoModel/internal/version.Version=…'`
  (works across module boundaries at link time).
- **Metrics**: core serves the default Prometheus registry at `/metrics`,
  so collectors registered with `promauto`/`prometheus.MustRegister` in your
  module appear automatically.
- **Config**: `config.Load()` ignores unknown top-level YAML keys, so an
  extension may read its own section of `config.yaml` and its own env vars
  with its own loader.
