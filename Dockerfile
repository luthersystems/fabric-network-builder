#
# byfn-vars builder
#
FROM debian:bullseye AS byfn-vars-builder

ARG BASEIMAGETAG=0.4.20
ARG IMAGETAG=2.5.4
ARG CAIMAGETAG=1.5.7
ARG IMAGENS=hyperledger

RUN apt-get update && apt-get install -y gettext-base && rm -rf /var/lib/apt/lists/*

COPY byfn-vars.sh.template /tmp/

ENV BASEIMAGETAG=${BASEIMAGETAG}
ENV IMAGETAG=${IMAGETAG}
ENV CAIMAGETAG=${CAIMAGETAG}
ENV IMAGENS=${IMAGENS}

RUN envsubst < /tmp/byfn-vars.sh.template > /tmp/byfn-vars.sh

#
# fabric artifacts builder
#
FROM debian:bullseye AS fabric-artifacts-builder

ARG FABRIC_VERSION=2.5.4
ARG FABRIC_CRYPTOGEN_VERSION=${FABRIC_VERSION}

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# Function to map uname -m to amd64 or arm64
RUN ARCH=$(uname -m) && \
    case "$ARCH" in \
      x86_64) ARCH="amd64" ;; \
      aarch64) ARCH="arm64" ;; \
      arm64) ARCH="arm64" ;; \
      *) echo "Unsupported architecture: $ARCH" && exit 1 ;; \
    esac && \
    apt-get update && apt-get install -y curl && rm -rf /var/lib/apt/lists/* \
  && mkdir -p /tmp/hyperledger/fabric \
  && cd /tmp/hyperledger/fabric \
  && curl -sSL "https://github.com/hyperledger/fabric/releases/download/v${FABRIC_VERSION}/hyperledger-fabric-linux-${ARCH}-${FABRIC_VERSION}.tar.gz" | tar xz \
  && if [ "${FABRIC_VERSION}" != "${FABRIC_CRYPTOGEN_VERSION}" ]; then \
  curl -sSL "https://github.com/hyperledger/fabric/releases/download/v${FABRIC_CRYPTOGEN_VERSION}/hyperledger-fabric-linux-${ARCH}-${FABRIC_CRYPTOGEN_VERSION}.tar.gz" | tar xz cryptogen; \
  fi

# FNB image
#
FROM python:3.12-bullseye

RUN apt-get update && apt-get install --no-install-recommends -y zip rsync gettext-base curl && rm -rf /var/lib/apt/lists/*

# Install Docker CLI (static binary) - version 27.3.1 supports API 1.44+
# This replaces the old docker.io package which had version 1.41
RUN ARCH=$(dpkg --print-architecture | sed 's/arm64/aarch64/; s/amd64/x86_64/') && \
    curl -fsSL https://download.docker.com/linux/static/stable/${ARCH}/docker-27.3.1.tgz | tar -xz -C /tmp && \
    mv /tmp/docker/docker /usr/local/bin/docker && \
    chmod +x /usr/local/bin/docker && \
    rm -rf /tmp/docker && \
    docker --version

ARG COMPOSE_VER=2.20.0
RUN curl -sSL "https://github.com/docker/compose/releases/download/v${COMPOSE_VER}/docker-compose-$(uname -s)-$(uname -m)" -o /usr/local/bin/docker-compose && chmod +x /usr/local/bin/docker-compose

RUN mkdir /network
WORKDIR /network
ENTRYPOINT ["fabric-network-builder"]
RUN ln -s /var/lib/fabric-network-builder/network.py /usr/bin/fabric-network-builder

ENV PATH=$PATH:/var/lib/fabric-network-builder/release/linux/bin

COPY requirements.txt /var/lib/fabric-network-builder/
RUN pip install --no-cache-dir -r /var/lib/fabric-network-builder/requirements.txt

COPY byfn.sh /var/lib/fabric-network-builder/
COPY template /var/lib/fabric-network-builder/template
COPY network.py /var/lib/fabric-network-builder/

RUN chmod -R +x \
  /var/lib/fabric-network-builder/network.py \
  /var/lib/fabric-network-builder/byfn.sh

COPY --from=fabric-artifacts-builder /tmp/hyperledger/fabric/bin/* /var/lib/fabric-network-builder/release/linux/bin/
COPY --from=byfn-vars-builder /tmp/byfn-vars.sh /var/lib/fabric-network-builder/
