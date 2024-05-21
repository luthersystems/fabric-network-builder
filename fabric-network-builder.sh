#!/bin/bash

VERSION=latest

if [[ "$FNB_DEV" == "true" ]]; then
    FNB_DEV_ROOT="$(dirname $(greadlink -f $0))"
    DEV_MOUNTS="-v ${FNB_DEV_ROOT}/byfn.sh:/var/lib/fabric-network-builder/byfn.sh:ro \
                -v ${FNB_DEV_ROOT}/template:/var/lib/fabric-network-builder/template:ro \
                -v ${FNB_DEV_ROOT}/network.py:/var/lib/fabric-network-builder/network.py:ro"
fi

DOCKER_TAG="$VERSION"
DOCKER_IMAGE=luthersystems/fabric-network-builder
END_USER=$(id -u $USER):$(id -g $USER)
DOCKER_WORKDIR=/network

PROJECT_PATH=$(pwd)
docker run --rm -it \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "$PROJECT_PATH:$PROJECT_PATH" \
    $DEV_MOUNTS \
    -w "$PROJECT_PATH" \
    $DOCKER_IMAGE:$DOCKER_TAG --chown $END_USER $@
