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

# Pass the builder's settings (FNB_*) and the variables it forwards to the
# chaincode containers (CHAINCODE_*, SUBSTRATE_*, OTEL_*) by name, so their
# values never show on the command line. (The ${a[@]+...} form keeps an
# empty array from failing set -u on the bash 3.2 of macOS.)
ENV_ARGS=()
while read -r name; do
    case "$name" in
    FNB_VERSION) ;;
    FNB_* | CHAINCODE_* | SUBSTRATE_* | OTEL_*) ENV_ARGS+=(-e "$name") ;;
    esac
done < <(compgen -e)

docker run --rm -it \
    -u "$(id -u):$(id -g)" --group-add "$SOCK_GID" \
    ${ENV_ARGS[@]+"${ENV_ARGS[@]}"} \
    -v "$SOCK:$SOCK" \
    -v "$PROJECT_PATH:$PROJECT_PATH" \
    -w "$PROJECT_PATH" \
    "$DOCKER_IMAGE" "$@"
