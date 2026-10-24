#!/usr/bin/env sh
# Pulls an official Docker Hub library image (postgres:18-alpine, mongo:7, ...)
# from the first registry that serves it and prints the reference that worked,
# so the caller runs exactly that image:
#
#   image=$(sh tools/ci/pull-image.sh postgres:18-alpine)
#
# A reference that already names a registry (public.ecr.aws/docker/library/
# mongo:7) is pulled as-is, with the same retry rounds.
#
# GitHub runners share egress IPs with every other repository, and registries
# meter anonymous pulls per IP: Docker Hub times out, ECR Public answers
# "toomanyrequests: Rate exceeded" (per second) or "Data limit exceeded" (per
# month). Retrying one registry does nothing against a used-up quota, so each
# round tries every registry in order before backing off.
#
# Environment (defaults in brackets):
#   IMAGE_REGISTRIES            space-separated prefixes, tried in order
#                               [mirror.gcr.io/library public.ecr.aws/docker/library docker.io/library]
#   IMAGE_PULL_ROUNDS           passes over the registries [3]
#   IMAGE_PULL_BACKOFF_SECONDS  backoff step, round n waits n*step [5]
#
# Progress goes to stderr; stdout carries only the pulled reference.
# tests/integration/docker_test.go keeps the same registry order for TestMain.
set -eu

if [ $# -ne 1 ]; then
	echo "usage: $0 name:tag" >&2
	exit 2
fi

name=$1
registries=${IMAGE_REGISTRIES:-mirror.gcr.io/library public.ecr.aws/docker/library docker.io/library}
rounds=${IMAGE_PULL_ROUNDS:-3}
backoff=${IMAGE_PULL_BACKOFF_SECONDS:-5}

# Docker's own rule: the first path component names a registry when it has a
# dot or a colon, or is localhost.
case $name in
*/*)
	case ${name%%/*} in
	*.* | *:* | localhost) registries= ;;
	esac
	;;
esac
if [ -n "$registries" ]; then
	refs=
	for registry in $registries; do
		refs="$refs $registry/$name"
	done
else
	refs=$name
fi

round=1
while :; do
	for ref in $refs; do
		if docker pull --quiet "$ref" >&2; then
			echo "$ref"
			exit 0
		fi
		echo "pull-image: pull of $ref failed" >&2
	done
	if [ "$round" -ge "$rounds" ]; then
		echo "pull-image: pull of $name failed on every registry after $rounds rounds" >&2
		exit 1
	fi
	echo "pull-image: round $round/$rounds failed, retrying in $((round * backoff))s" >&2
	sleep $((round * backoff))
	round=$((round + 1))
done
