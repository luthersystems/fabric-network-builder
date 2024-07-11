PROJECT_REL_DIR ?= .
PROJECT_ABS_DIR=$(abspath ${PROJECT_REL_DIR})

include ${PROJECT_REL_DIR}/common.config.mk

LICENSE_FILE=${PROJECT_ABS_DIR}/.luther-license.yaml

# VERSION is overridden by github actions
VERSION ?= 2.186.0-fabric2-SNAPSHOT

BUILD_ID=$(shell git rev-parse --short HEAD)
BUILD_VERSION=${VERSION}$(if $(findstring SNAPSHOT,${VERSION}),-${BUILD_ID},)
PACKAGE=${PROJECT_PATH}
FQ_DOCKER_IMAGE ?= luthersystems/$(2)
FABRIC_IMAGE_TAG=${FABRIC_VERSION}

TAG_SUFFIX ?= -amd64

DOCKER_IN_DOCKER_MOUNT?=-v /var/run/docker.sock:/var/run/docker.sock

ifeq ($(OS),Windows_NT)
	IS_WINDOWS=1
endif

CP=cp
RM=rm
LN ?= gln
READLINK ?= greadlink
DOCKER=docker

DOCKER_RUN_OPTS=-i --rm
DOCKER_RUN=${DOCKER} run ${DOCKER_RUN_OPTS}
CHOWN=chown
CHOWN_USR=$(LOGNAME)
CHOWN_USR?=$(USER)
CHOWN_GRP=$(if $(or $(IS_WINDOWS),$(GOMODFIX)),,admin)
DOCKER_USER="$(shell id -u ${CHOWN_USR}):$(shell id -g ${CHOWN_USR})"
MKDIR_P=mkdir -p
TOUCH=touch
GZIP=gzip
GUNZIP=gunzip

# The Makefile determines whether to build a container or not by consulting a
# dummy file that is touched whenever the container is built.  The function,
# IMAGE_DUMMY, computes the path to the dummy file.
DUMMY_TARGET=build/$(1)/$(2)/.dummy
IMAGE_DUMMY=$(call DUMMY_TARGET,image,$(1))
PUSH_DUMMY=$(call DUMMY_TARGET,push,$(1))

UNAME=$(shell uname)
GIT_LS_FILES=$(shell git ls-files $(1))


DOCKER_WIN_DIR=$(shell cygpath -wm $(realpath $(1)))
DOCKER_NIX_DIR=$(realpath $(1))
DOCKER_DIR=$(if $(IS_WINDOWS),$(call DOCKER_WIN_DIR, $(1)),$(call DOCKER_NIX_DIR, $(1)))

MODULE_TO_SUBSTRATE=./../..

# print out make variables, e.g.:
# make echo:VERSION
echo\:%:
	@echo $($*)

# Check if the requested image exists locally then pull it if necessary.
# NOTE: The / is necessary to prevent automatic path splitting on the target
# names.
docker-pull/%: id=$(shell docker image inspect -f "{{.Id}}" $* 2>/dev/null)
docker-pull/%:
	@[[ -n "${id}" ]] || { echo "retrieving $*" && docker pull $*; }

SHELL_MAKEC=$(shell $(call MAKEC,$(1),$(2)))

make-C/%:
	@cd $(dir $*) && $(MAKE) $(notdir $*)
