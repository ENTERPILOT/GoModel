#!/usr/bin/env sh
# Runs a single-node MongoDB replica set for the store suites (see
# internal/storage/mongotest). A replica set, not a bare mongod: the guardrails
# and workflows stores use transactions. GitHub service containers cannot pass
# --replSet, so the container is started here instead.
#
#   mongo-replset.sh start   pull and start the container, detached from the
#                            calling step so the image pull overlaps the Go
#                            setup and compile instead of gating the job
#   mongo-replset.sh wait    block until the container runs, initiate the
#                            replica set, and wait for a primary
#
# The server has no authentication, so the port is published on loopback
# only. MONGO_PORT (default 27017) picks the port; MONGO_WAIT_SECONDS
# (default 180) bounds the wait.
#
# The suites read MONGO_TEST_DSN; the workflow sets it to
# mongodb://localhost:27017/?replicaSet=rs&directConnection=true.
set -eu

name=gomodel-mongo
image=public.ecr.aws/docker/library/mongo:7
log=/tmp/gomodel-mongo-start.log
deadline_seconds=${MONGO_WAIT_SECONDS:-180}
# MONGO_PORT lets a developer run this next to another MongoDB; the DSN in
# MONGO_TEST_DSN must then use the same port.
port=${MONGO_PORT:-27017}

mongosh() {
	docker exec "$name" mongosh --quiet --eval "$1"
}

case "${1:-}" in
start)
	# setsid puts the pull in its own session so it survives the calling step
	# ending; macOS has no setsid, so fall back to nohup alone there.
	if command -v setsid >/dev/null 2>&1; then
		setsid nohup sh -c "docker run -d --rm --name $name -p 127.0.0.1:$port:27017 $image --replSet rs --bind_ip_all" >"$log" 2>&1 &
	else
		nohup sh -c "docker run -d --rm --name $name -p 127.0.0.1:$port:27017 $image --replSet rs --bind_ip_all" >"$log" 2>&1 &
	fi
	echo "mongo-replset: starting $name in the background"
	;;
wait)
	started=$(date +%s)
	until [ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null || true)" = "true" ] \
		&& mongosh 'db.adminCommand({ ping: 1 }).ok' >/dev/null 2>&1; do
		if [ $(( $(date +%s) - started )) -ge "$deadline_seconds" ] || grep -qi 'error\|not found' "$log" 2>/dev/null; then
			echo "mongo-replset: $name did not come up (waited $(( $(date +%s) - started ))s)" >&2
			cat "$log" 2>/dev/null || true
			docker logs "$name" 2>&1 | tail -20 || true
			exit 1
		fi
		sleep 1
	done
	mongosh "rs.initiate({ _id: 'rs', members: [{ _id: 0, host: 'localhost:27017' }] })" >/dev/null
	until mongosh 'rs.isMaster().ismaster' 2>/dev/null | grep -q true; do
		if [ $(( $(date +%s) - started )) -ge "$deadline_seconds" ]; then
			echo "mongo-replset: no primary within ${deadline_seconds}s" >&2
			exit 1
		fi
		sleep 1
	done
	echo "mongo-replset: primary ready after $(( $(date +%s) - started ))s"
	;;
*)
	echo "usage: $0 start|wait" >&2
	exit 2
	;;
esac
