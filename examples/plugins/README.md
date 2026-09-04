# Example GoModel plugins

Each directory here is a `package main` that builds into a shared object
(`.so`) GoModel can load at startup. `keywordblock/` is a complete,
commented example implementing the `prompt` and `response` hooks.

## Build and load an example

```sh
make example-plugins            # builds every example into ./plugins/<dir>.so
# or one at a time:
go run ./cmd/gomodel plugin build ./examples/plugins/keywordblock -o plugins/keyword_block.so
go run ./cmd/gomodel plugin inspect plugins/keyword_block.so
```

Point GoModel at the directory and list the files to load:

```yaml
plugins:
  search_paths: ["./plugins"]          # PLUGINS_SEARCH_PATHS=./plugins
  load:
    - file: keyword_block.so
      sha256: "<shasum -a 256 plugins/keyword_block.so>"   # optional pin
```

Loading needs a cgo-enabled GoModel on Linux, macOS, or FreeBSD:
`make build-plugins` (produces `bin/gomodel-plugins`) or the
`gomodel:<version>-plugins` image (`make image-plugins`, `Dockerfile.plugins`).
The default static binary and image refuse `.so` files with a clear error.

## The exact-toolchain rule

Go's `plugin` package only opens a shared object built with the **same Go
version**, the **same build flags** (`-trimpath`, `-race`, `-tags`), and
**identical sources of every shared package**: the standard library and
`github.com/enterpilot/gomodel/pluginapi`. Nothing else in GoModel is shared
with a plugin, so internal changes never affect one, but every GoModel
release and every Go toolchain update (patch releases included) requires a
rebuild.

`gomodel plugin build` makes that mechanical: it copies the flags recorded in
the `gomodel` binary that runs it, forces `CGO_ENABLED=1`, pins
`GOTOOLCHAIN` to the host's Go version (so a different local Go downloads the
matching release), stamps a `GoModelBuildInfo` variable into the plugin, and
refuses an output whose Go version differs from the host's. Always build with
the binary that will load the plugin. In Docker, build the `plugin-builder`
target of `Dockerfile.plugins` and run it against your plugin directory.

A refused load names both sides, for example:

```
plugin file /app/plugins/x.so was built with a different toolchain, flags, or
pluginapi sources: it was built with go1.27.0, gomodel v0.1.90, flags -trimpath;
this binary was built with go1.27.0, gomodel v0.1.91, flags (none). Rebuild it
with `gomodel plugin build` from this GoModel version
```

## Writing a plugin in its own module

A plugin does not have to live in the GoModel tree. Create a module that
requires GoModel at the version of the binary that will load it and imports
only `pluginapi`:

```sh
mkdir acme-guard && cd acme-guard
go mod init example.com/acme-guard
go get github.com/enterpilot/gomodel@v0.1.91     # the host's `gomodel --version`
```

```go
// main.go
package main

import (
	"context"
	"encoding/json"

	"github.com/enterpilot/gomodel/pluginapi"
)

func GoModelPlugin() pluginapi.Plugin { return &guard{} }

type guard struct{}

func (g *guard) Manifest() pluginapi.Manifest {
	return pluginapi.Manifest{Name: "acme_guard", Version: "0.1.0", Kinds: []pluginapi.Kind{pluginapi.KindPrompt}}
}
func (g *guard) Init(context.Context, json.RawMessage, pluginapi.Host) error { return nil }
func (g *guard) Close(context.Context) error                                { return nil }
func (g *guard) OnPrompt(context.Context, *pluginapi.Exchange) (pluginapi.Decision, error) {
	return pluginapi.Allow(), nil
}

func main() {}
```

For local development against a GoModel checkout, point the module at it:

```
// go.mod
replace github.com/enterpilot/gomodel => ../gomodel
```

Build with the host binary and inspect the result:

```sh
gomodel plugin build . -o acme_guard.so
gomodel plugin inspect acme_guard.so
```

Rules of thumb:

- Export `func GoModelPlugin() pluginapi.Plugin` (preferred: one file, many
  configured instances). A `var GoModelPlugin pluginapi.Plugin` also works
  but limits the file to a single configured instance.
- Import only `pluginapi` from GoModel. Anything else drags internal packages
  into the shared set and makes rebuilds fragile.
- Pin the GoModel version in `go.mod` to the host's release and rebuild on
  every GoModel or Go upgrade; make it a CI step.
- Treat a `.so` as trusted code: loading one is equivalent to changing the
  binary. Keep `search_paths` root-owned and pin `sha256` in production.
