PROJECT_REL_DIR=.
include ${PROJECT_REL_DIR}/common.mk

DOCKER_IMAGE=luthersystems/fabric-network-builder
DOCKER_IMAGE_PUBLIC_FQN=docker.io/${DOCKER_IMAGE}

# ko builds the image (.ko.yaml). Its ldflags read the default Fabric image
# tags of the generated network from the environment.
export FABRIC_VERSION FABRIC_CA_VERSION FABRIC_BASE_VERSION
KO ?= ko
KO_PLATFORM ?= linux/$(shell go env GOARCH)

.PHONY: default
default: docker-build
	@

.PHONY: clean
clean:
	rm -fr build

.PHONY: test
test:
	go test ./...

# The CI Go job and a local check run the same linter as the org's shared
# Quality workflow.
.PHONY: static-checks
static-checks:
	golangci-lint run ./...

# docker-build loads the image for one platform (KO_PLATFORM, default the
# host's) into the local daemon as ${DOCKER_IMAGE}:latest.
.PHONY: docker-build
docker-build:
	ref=$$(KO_DOCKER_REPO=ko.local ${KO} build --bare --platform=${KO_PLATFORM} ./cmd/fabric-network-builder) && \
		${DOCKER} tag "$$ref" ${DOCKER_IMAGE}:latest

# docker-push publishes the amd64 and arm64 images and their manifest list to
# Docker Hub as ${BUILD_VERSION} and latest. Log in to Docker Hub first.
.PHONY: docker-push
docker-push:
	KO_DOCKER_REPO=${DOCKER_IMAGE_PUBLIC_FQN} ${KO} build --bare \
		--platform=linux/amd64,linux/arm64 \
		--tags=${BUILD_VERSION},latest \
		./cmd/fabric-network-builder
