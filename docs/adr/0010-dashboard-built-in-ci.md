# ADR-0010: The dashboard is built in CI, not committed

## Context

The admin dashboard is a Svelte app in `web/dashboard/`. Vite builds it into
`internal/admin/dashboard/static/dist/`, which `//go:embed` compiles into the
binary. Until this decision the build output was committed, and CI verified
that the committed output matched the sources.

That arrangement has two problems.

**Review.** A PR that touches the dashboard carries a minified JavaScript
bundle of several hundred kilobytes. Reviewers cannot read it and GitHub
collapses it. The "build is current" CI check proves the bundle matches the
sources *on the CI runner*, but a reviewer approving the PR is still merging
bytes they did not inspect, and the guarantee depends on that check never
being skipped, disabled, or bypassed. The committed bundle is the one place in
the repository where a contributor could ship code that no human reads.

**Conflicts.** Vite content-hashes its output, so every dashboard change
produces a new `assets/index-<hash>.js`, a new `.css`, and a rewritten
`index.html`. Any two open PRs that touch the dashboard conflict with each
other on files that cannot be merged, and the second one must be rebuilt after
the first merges. With several dashboard PRs open at once this was a steady
tax on every contributor.

The reason the output was committed in the first place is that `go build` on a
fresh checkout should work without Node, and that the embedded assets should
be reproducible from a tag. Both are worth keeping.

## Decision

`static/dist` is no longer committed. It is ignored by git and produced by a
dedicated `frontend` job in GitHub Actions:

- The job runs with `persist-credentials: false`, has no secrets, uses a pinned
  Node version and pinned action SHAs, installs from `package-lock.json` with
  `npm ci`, and uploads `static/dist` as a workflow artifact.
- Every job that needs the dashboard — Go tests, the build check, the Docker
  image, GoReleaser — downloads that artifact instead of building its own.
  The bytes in a release are exactly the bytes the secretless job produced.
- The `Dockerfile` stays Go-only; the Docker build context contains the
  downloaded `dist`, and the image never runs Node.
- A committed placeholder in `static/` keeps the `//go:embed` compiling on a
  clean checkout (Vite empties `dist/` on every build, so it lives one level
  up). A binary built without the dashboard fails at
  startup when the UI is enabled, with a message pointing at `make frontend`.
  It does not serve a half-working dashboard.
- Locally, `make frontend` builds the dashboard once; `make build` and
  `make image` depend on it.

The trust boundary moves from "a reviewer looked at the bundle" (which nobody
did) to "the bundle came from these sources, this lockfile, and this pinned
toolchain". The inputs that determine the output are all text files that are
reviewed like any other code. Changes to `package-lock.json` deserve the same
attention as changes to `go.sum`.

## Consequences

- PRs that change the dashboard contain only Svelte/JS/CSS sources. They no
  longer conflict with each other over build output, and the review diff is
  the actual change.
- `go install github.com/enterpilot/gomodel/cmd/gomodel@latest` produces a
  binary without the dashboard. This path was never documented; supported
  installs (`install.sh`, Docker, GitHub releases) all go through CI.
- A fresh clone needs Node once (`make frontend`) before `go run` with the UI
  enabled. Backend-only work with `ADMIN_UI_ENABLED=false` still needs no
  Node.
- CI gains one job that the Go jobs wait on, roughly a minute of added
  latency. Node dependencies install once per workflow run instead of once per
  dashboard job.
- The "committed build is current" check is removed. It has nothing left to
  check.

## Revisit when

- A supported install path needs `go install` to produce a complete binary. The
  fix would be a release job that commits `dist` to a tag or a separate
  branch, which is a different trade-off from committing it in PRs.
- The `frontend` job needs a secret for any reason. That would undo the
  property this decision exists for and should be treated as a design
  problem, not a configuration change.
