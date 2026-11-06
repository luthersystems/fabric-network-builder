#!/bin/bash
# Runs the builder image on the current directory as your own user, with
# the Docker socket's group so that up, install, invoke and down can use
# the socket. The files it writes belong to you.
set -euo pipefail

DOCKER_IMAGE=luthersystems/fabric-network-builder:${FNB_VERSION:-latest}
SOCK=/var/run/docker.sock
PROJECT_PATH=$(pwd)

# The socket's group as the containers see it. On Docker Desktop (macOS)
# it differs from the host file's group, so ask a container.
SOCK_GID=$(docker run --rm --entrypoint stat -v "$SOCK:$SOCK" "$DOCKER_IMAGE" -c %g "$SOCK")

docker run --rm -it \
    -u "$(id -u):$(id -g)" --group-add "$SOCK_GID" \
    -v "$SOCK:$SOCK" \
    -v "$PROJECT_PATH:$PROJECT_PATH" \
    -w "$PROJECT_PATH" \
    "$DOCKER_IMAGE" "$@"
