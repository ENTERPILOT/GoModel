#!/usr/bin/env sh
# Runs a single-node MongoDB replica set for the store suites (see
# internal/storage/mongotest). A replica set, not a bare mongod: the guardrails
# and workflows stores use transactions. GitHub service containers cannot pass
# --replSet, so the container is started here instead.
#
#   mongo-replset.sh start   pull and start the container in the background,
#                            so the pull overlaps the Go setup and compile
#                            instead of gating the job
#   mongo-replset.sh wait    block until the container runs, initiate the
#                            replica set, and wait for a primary
#
# The image comes from tools/ci/pull-image.sh, which falls over between
# registries and backs off between rounds (IMAGE_PULL_* there). `start` records
# the outcome in a state file and `wait` fails only on that outcome, not on a
# transient error line.
#
# The server has no authentication, so the port is published on loopback only.
# Environment (defaults in brackets):
#   MONGO_PORT                  published port [27017]
#   MONGO_WAIT_SECONDS          bound on `wait` [180]
#   MONGO_IMAGE                 library image [mongo:7]
#   MONGO_STATE_DIR             where the start log and state file go [/tmp]
#
# The suites read MONGO_TEST_DSN; the workflow sets it to
# mongodb://localhost:27017/?replicaSet=rs&directConnection=true.
set -eu

name=gomodel-mongo
image=${MONGO_IMAGE:-mongo:7}
state_dir=${MONGO_STATE_DIR:-/tmp}
log=$state_dir/gomodel-mongo-start.log
state=$state_dir/gomodel-mongo-start.state
deadline_seconds=${MONGO_WAIT_SECONDS:-180}
port=${MONGO_PORT:-27017}

mongosh() {
	docker exec "$name" mongosh --quiet --eval "$1"
}

# pull_and_run is the background half of `start`. It writes "started" or
# "failed" to the state file when it finishes.
pull_and_run() {
	if ! ref=$(sh "$(dirname "$0")/pull-image.sh" "$image"); then
		echo failed >"$state"
		return 1
	fi
	if ! docker run -d --rm --name "$name" -p "127.0.0.1:$port:27017" "$ref" --replSet rs --bind_ip_all; then
		echo failed >"$state"
		return 1
	fi
	echo started >"$state"
}

case "${1:-}" in
start)
	rm -f "$state"
	# setsid keeps the background pull alive after the calling step's shell
	# exits; macOS has no setsid, so fall back to nohup alone there.
	if command -v setsid >/dev/null 2>&1; then
		setsid nohup sh "$0" __pull_and_run >"$log" 2>&1 &
	else
		nohup sh "$0" __pull_and_run >"$log" 2>&1 &
	fi
	echo "mongo-replset: starting $name in the background"
	;;
__pull_and_run)
	pull_and_run
	;;
wait)
	started=$(date +%s)
	fail() {
		echo "mongo-replset: $1 (waited $(($(date +%s) - started))s)" >&2
		cat "$log" 2>/dev/null || true
		docker logs "$name" 2>&1 | tail -20 || true
		exit 1
	}
	until [ "$(cat "$state" 2>/dev/null || true)" = "started" ] \
		&& [ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null || true)" = "true" ] \
		&& mongosh 'db.adminCommand({ ping: 1 }).ok' >/dev/null 2>&1; do
		[ "$(cat "$state" 2>/dev/null || true)" = "failed" ] && fail "$name did not start"
		[ $(($(date +%s) - started)) -ge "$deadline_seconds" ] && fail "$name did not come up in time"
		sleep 1
	done
	mongosh "rs.initiate({ _id: 'rs', members: [{ _id: 0, host: 'localhost:27017' }] })" >/dev/null
	until mongosh 'rs.isMaster().ismaster' 2>/dev/null | grep -q true; do
		[ $(($(date +%s) - started)) -ge "$deadline_seconds" ] && fail "no primary in time"
		sleep 1
	done
	echo "mongo-replset: primary ready after $(($(date +%s) - started))s"
	;;
*)
	echo "usage: $0 start|wait" >&2
	exit 2
	;;
esac
