#!/bin/sh

# Supervise the Go gateway and Vite dev server as one local process. Vite is
# browser-facing on :5173 and proxies API requests to the gateway on :8080.
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
backend_port=${PORT:-8080}
frontend_port=${FRONTEND_PORT:-5173}
backend_pid=
frontend_pid=

cleanup() {
	trap - EXIT INT TERM
	if [ -n "$frontend_pid" ]; then
		kill -TERM "$frontend_pid" 2>/dev/null || true
	fi
	if [ -n "$backend_pid" ]; then
		kill -TERM "$backend_pid" 2>/dev/null || true
	fi
	if [ -n "$frontend_pid" ]; then
		wait "$frontend_pid" 2>/dev/null || true
	fi
	if [ -n "$backend_pid" ]; then
		wait "$backend_pid" 2>/dev/null || true
	fi
}

trap cleanup EXIT
# Ctrl+C is an expected development shutdown, not a failed run.
trap 'exit 0' INT TERM

"$repo_dir/bin/gomodel" &
backend_pid=$!

(
	cd "$repo_dir/web/dashboard"
	export GOMODEL_DEV_PORT=$frontend_port
	export GOMODEL_DEV_PROXY=${GOMODEL_DEV_PROXY:-http://localhost:$backend_port}
	# Replace this subshell with Vite so frontend_pid always identifies the
	# actual server process. Killing an npm wrapper can orphan its Vite child.
	exec ./node_modules/.bin/vite
) &
frontend_pid=$!

echo "Dashboard: http://localhost:$frontend_port/admin/dashboard (API proxied to :$backend_port)"

# Exit when either child exits; the EXIT trap stops the other one. This also
# catches occupied ports and startup failures instead of leaving half the
# development stack running.
while kill -0 "$backend_pid" 2>/dev/null && kill -0 "$frontend_pid" 2>/dev/null; do
	sleep 1
done

status=0
if ! kill -0 "$backend_pid" 2>/dev/null; then
	wait "$backend_pid" || status=$?
else
	wait "$frontend_pid" || status=$?
fi
exit "$status"
