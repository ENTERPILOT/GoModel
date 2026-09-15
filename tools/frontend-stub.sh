#!/usr/bin/env sh
# Copies the placeholder dashboard bundle from tools/fixtures/dashboard-stub
# into internal/admin/dashboard/static/dist so the Go test suites, which
# //go:embed that directory, compile and pass without Node. The three tests
# that read the bundle only check that index.html references hashed assets
# that resolve, so this is enough.
#
# It never replaces a real build, and it is never what ships: `make build`,
# the Docker image, and the release workflow all build the real bundle
# (docs/adr/0010-dashboard-built-in-ci.md).
set -eu

dist=internal/admin/dashboard/static/dist
if [ -f "$dist/index.html" ]; then
	echo "frontend-stub: $dist/index.html exists; leaving the dashboard bundle alone"
	exit 0
fi

mkdir -p "$dist"
cp -R "$(dirname "$0")/fixtures/dashboard-stub/." "$dist/"
echo "frontend-stub: wrote a placeholder dashboard bundle to $dist"
