#!/bin/bash

VERSION=latest

DOCKER_TAG="$VERSION"
DOCKER_IMAGE=luthersystems/fabric-network-builder
END_USER=$(id -u $USER):$(id -g $USER)
DOCKER_WORKDIR=/network

PROJECT_PATH=$(pwd)
docker run --rm -it -u root \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$PROJECT_PATH:$PROJECT_PATH" \
    -w "$PROJECT_PATH" \
    "$DOCKER_IMAGE:$DOCKER_TAG" --chown "$END_USER" "$@"
