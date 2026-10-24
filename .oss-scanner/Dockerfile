# Image for Anthropic's OSS Scanner (github.com/anthropics/oss-scanner, projects/gomodel). The scanner builds
# this with the repository as the context, with network access, then audits the image offline. Everything
# the build and the tests need must be fetched here.
#
# Dependabot bumps both images with the root Dockerfile. Keep Go at the go.mod version (the golang image sets
# GOTOOLCHAIN=local, so an older image fails the build) and Node at the version CI uses.
FROM node:22.23.2-bookworm AS node

FROM golang:1.27.3-bookworm

# Node builds the embedded Svelte dashboard and runs its JS tests.
COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm

# scanner contract: the checkout lives inside the image, at /src
COPY . /src
WORKDIR /src

RUN go mod download
RUN cd web/dashboard && npm ci --no-audit --no-fund --ignore-scripts

# Build the real dashboard bundle (the binary embeds it), then the gateway at /src/bin/gomodel.
RUN cd web/dashboard && npm run build
RUN go build ./... && go build -o /src/bin/gomodel ./cmd/gomodel

# Warm the build cache with every test binary, including the in-process e2e and contract suites.
# -vet=off: a vet finding must not fail the build.
RUN (go test -vet=off -run '^$' ./... || echo "WARNING: some test binaries do not compile") \
    && (go test -vet=off -tags=e2e,contract -run '^$' ./tests/e2e/... ./tests/contract/... || echo "WARNING: e2e/contract test binaries do not compile") \
    && (go test -vet=off -race -run '^$' ./cmd/... ./config/... ./ext/... ./internal/... ./run/... || echo "WARNING: some test binaries do not compile with -race")

# Run the unit suite so the log shows the image works; a failing or hung test must not fail the build.
RUN (timeout 1500 go test -vet=off -count=1 -timeout 20m ./cmd/... ./config/... ./ext/... ./internal/... ./run/... \
    || echo "WARNING: go test reported failures")

# The audit is offline: no module proxy, no checksum database, no toolchain download.
ENV GOPROXY=off \
    GOSUMDB=off \
    GOTOOLCHAIN=local
