#!/usr/bin/env sh
# Runs PostgreSQL for the CI test jobs. A step rather than a service container:
# the runner pulls a service image from a single registry and fails the job
# before any step runs when that pull fails. Here the image comes from
# tools/ci/pull-image.sh, which falls over to the next registry.
#
#   postgres.sh start   pull and start the container, replacing one left
#                       from an earlier run
#   postgres.sh wait    block until it accepts TCP connections
#
# The container is not started with --rm, so a server that dies during startup
# keeps its logs for `wait` to print. Locally, remove it with
# `docker rm -f gomodel-postgres`.
#
# `wait` checks over TCP on purpose: during initdb the image runs a temporary
# server on the Unix socket only, which a socket check would report as ready.
#
# The server has a fixed password, so the port is published on loopback only.
# Environment (defaults in brackets):
#   POSTGRES_IMAGE         library image [postgres:18-alpine]
#   POSTGRES_USER          [gomodel]
#   POSTGRES_PASSWORD      [gomodel]
#   POSTGRES_DB            [gomodel]
#   POSTGRES_PORT          published port [5432]
#   POSTGRES_WAIT_SECONDS  bound on `wait` [120]
set -eu

name=gomodel-postgres
image=${POSTGRES_IMAGE:-postgres:18-alpine}
user=${POSTGRES_USER:-gomodel}
password=${POSTGRES_PASSWORD:-gomodel}
db=${POSTGRES_DB:-gomodel}
port=${POSTGRES_PORT:-5432}
deadline_seconds=${POSTGRES_WAIT_SECONDS:-120}

case "${1:-}" in
start)
	ref=$(sh "$(dirname "$0")/pull-image.sh" "$image")
	docker rm -f "$name" >/dev/null 2>&1 || true
	docker run -d --name "$name" -p "127.0.0.1:$port:5432" \
		-e POSTGRES_USER="$user" -e POSTGRES_PASSWORD="$password" -e POSTGRES_DB="$db" \
		"$ref" >/dev/null
	echo "postgres: started $name from $ref"
	;;
wait)
	started=$(date +%s)
	fail() {
		echo "postgres: $1 (waited $(($(date +%s) - started))s)" >&2
		docker logs "$name" 2>&1 | tail -20 || true
		exit 1
	}
	until docker exec "$name" pg_isready -q -h 127.0.0.1 -U "$user" -d "$db" 2>/dev/null; do
		[ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null || true)" = "true" ] || fail "$name is not running"
		[ $(($(date +%s) - started)) -ge "$deadline_seconds" ] && fail "$name did not accept connections in time"
		sleep 1
	done
	echo "postgres: ready after $(($(date +%s) - started))s"
	;;
*)
	echo "usage: $0 start|wait" >&2
	exit 2
	;;
esac
