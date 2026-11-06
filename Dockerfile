#
# build-harden kit (luthersystems/buildenv images/harden), run last in the
# final stage
#
FROM luthersystems/build-harden:v0.1.53@sha256:f0830ffa253727ce69abe815e65434a5fc16973594b37c938774bd6e1ff4e4e6 AS harden

#
# Build the builder. It is one static binary: cryptogen, configtxgen, the
# peer and osnadmin admin calls and Docker Compose all run in process, so
# the image needs no Fabric, docker or compose binary.
#
FROM golang:1.27.3-trixie@sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80 AS build

# The default image tags of the generated network (common.config.mk).
ARG IMAGETAG=2.5.16
ARG CAIMAGETAG=1.5.21
ARG BASEIMAGETAG=0.4.22
ARG IMAGENS=hyperledger

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN pkg=github.com/luthersystems/fabric-network-builder/internal/cli && \
    CGO_ENABLED=0 go build -trimpath -o /out/fabric-network-builder \
      -ldflags "-s -w \
        -X ${pkg}.ImageTag=${IMAGETAG} \
        -X ${pkg}.CAImageTag=${CAIMAGETAG} \
        -X ${pkg}.BaseImageTag=${BASEIMAGETAG} \
        -X ${pkg}.ImageNS=${IMAGENS}" \
      ./cmd/fabric-network-builder

#
# FNB image
#
FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2

# Consumers run their own scripts in this image: ui-core runs
# sandbox-infrastructure-template's fabric/fnb-gen.sh with bash, which uses
# zip, coreutils (base64, tr), grep, sed and git. Keep them.
RUN apt-get update && \
    apt-get install -y --no-install-recommends bash ca-certificates coreutils git zip && \
    rm -rf /var/lib/apt/lists/*

COPY --from=build /out/fabric-network-builder /usr/bin/fabric-network-builder

WORKDIR /network
ENTRYPOINT ["fabric-network-builder"]

# Harden the image with the build-harden kit: it removes each distrusted CA
# (such as the eMudhra/emSign roots, CVE-2026-111943) from the system trust
# store. This must stay the last step that changes files: a later apt or COPY
# step can restore the roots.
RUN --mount=type=bind,from=harden,source=/harden,target=/harden sh /harden/harden.sh
