PROJECT_REL_DIR=./../
include ${PROJECT_REL_DIR}/common.mk

DOCKER_IMAGE=luthersystems/fabric-network-builder
DOCKER_IMAGE_PUBLIC_FQN=docker.io/${DOCKER_IMAGE}

FABRIC_VERSION=2.5.4
FABRIC_BASE_VERSION=${SUBSTRATE_FABRIC_BASE_VERSION}
FABRIC_CA_VERSION=${SUBSTRATE_FABRIC_CA_VERSION}
FABRIC_CRYPTOGEN_VERSION=${FABRIC_VERSION}

FABRIC_IMAGE_TAG=$(FABRIC_VERSION)
FABRIC_BASE_IMAGE_TAG=$(FABRIC_BASE_VERSION)
FABRIC_CA_IMAGE_TAG=$(FABRIC_CA_VERSION)
FABRIC_IMAGE_NS=hyperledger

DOCKER_IMAGE_TARGET=build/images/${DOCKER_IMAGE}/${VERSION}/.dummy

.PHONY: default
default: docker-build
	@

.PHONY: clean
clean:
	rm -fr build

.PHONY: docker-build
docker-build: ${DOCKER_IMAGE_TARGET}
	@

.PHONY: docker-push
docker-push: docker-build
	docker push ${DOCKER_IMAGE_PUBLIC_FQN}:${BUILD_VERSION}
	docker push ${DOCKER_IMAGE_PUBLIC_FQN}:latest

${DOCKER_IMAGE_TARGET}: Dockerfile Makefile
	docker build \
		--build-arg FABRIC_VERSION=${FABRIC_VERSION} \
		--build-arg FABRIC_CRYPTOGEN_VERSION=${FABRIC_CRYPTOGEN_VERSION} \
		--build-arg BASEIMAGETAG=${FABRIC_BASE_IMAGE_TAG} \
		--build-arg IMAGETAG=${FABRIC_IMAGE_TAG} \
		--build-arg CAIMAGETAG=${FABRIC_CA_IMAGE_TAG} \
		--build-arg IMAGENS=${FABRIC_IMAGE_NS} \
		-t ${DOCKER_IMAGE} .
	docker tag ${DOCKER_IMAGE}:latest ${DOCKER_IMAGE}:${BUILD_VERSION}
	mkdir -p $(dir $@)
	touch $@
