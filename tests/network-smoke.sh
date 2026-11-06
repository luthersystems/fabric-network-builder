#!/usr/bin/env bash
# network-smoke.sh generates a two-org network with the builder image, brings
# it up on one Fabric release, and checks that the orderer and every peer
# joined the channel. It runs the builder the way a consumer does (docker run
# with the Docker socket and the project directory at the same path).
#
# Why: the network has no system channel. The orderer joins the channel with
# osnadmin and the peers with its genesis block, so a release that changes
# that flow (Fabric 3 removed the system channel) breaks `up`, not `generate`,
# and only a real network shows it (luthersystems/substrate#1034).
#
# Usage: tests/network-smoke.sh IMAGE FABRIC_TAG
#   FNB_SMOKE_DOCKER_OPTS  extra `docker run` options, for example -v mounts
#                          of a checkout's byfn.sh, template and network.py
set -o errexit
set -o nounset
set -o pipefail

image=${1:?usage: $0 IMAGE FABRIC_TAG}
fabric_tag=${2:?usage: $0 IMAGE FABRIC_TAG}
channel=luther

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
mkdir -p "${repo}/build"
work=$(mktemp -d "${repo}/build/smoke.XXXXXX")

fnb() {
  # -t: byfn.sh runs `docker exec -it cli ...`, which needs a TTY.
  # shellcheck disable=SC2086
  docker run --rm -t \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "${work}:${work}" -w "${work}" \
    -e FNB_IMAGETAG="${fabric_tag}" \
    ${FNB_SMOKE_DOCKER_OPTS:-} \
    "${image}" --chown "$(id -u):$(id -g)" --channel "${channel}" --force "$@"
}

cleanup() {
  fnb down >/dev/null 2>&1 || true
  rm -rf "${work}"
}
trap cleanup EXIT

fnb generate --domain=luther.systems --cc-name=smoke --org-count=2 --peer-count=2
test -f "${work}/channel-artifacts/${channel}.block"
test ! -e "${work}/channel-artifacts/genesis.block"

# `script` gives `docker run -t` a TTY when stdin is not one (CI).
if [ -t 0 ]; then
  fnb -s goleveldb up --cc-version smoke
else
  script -q -e -c "$(declare -f fnb); work='${work}' image='${image}' fabric_tag='${fabric_tag}' channel='${channel}' FNB_SMOKE_DOCKER_OPTS='${FNB_SMOKE_DOCKER_OPTS:-}' fnb -s goleveldb up --cc-version smoke" /dev/null
fi

running=$(docker inspect -f '{{.Config.Image}}' orderer0.luther.systems)
echo "orderer image: ${running}"
case "${running}" in
  *":${fabric_tag}") ;;
  *) echo "orderer runs ${running}, want tag ${fabric_tag}" >&2; exit 1 ;;
esac

# Ask the orderer itself: the peers join from the genesis block without it.
admin=/crypto-config/ordererOrganizations/luther.systems/users/Admin@luther.systems/tls
ocheck=$(docker exec cli osnadmin channel list --channelID "${channel}" \
  -o orderer0.luther.systems:7053 \
  --ca-file /crypto-config/ordererOrganizations/luther.systems/orderers/orderer0.luther.systems/msp/tlscacerts/tlsca.luther.systems-cert.pem \
  --client-cert "${admin}/client.crt" --client-key "${admin}/client.key" 2>&1)
echo "${ocheck}"
grep -q '"status": "active"' <<<"${ocheck}" || { echo "orderer0 is not an active member of ${channel}" >&2; exit 1; }

for org in 1 2; do
  for peer in 0 1; do
    host=peer${peer}.org${org}.luther.systems
    # The peer's own identity is a member of its org, which is all
    # `peer channel list` (cscc GetChannels) needs.
    out=$(docker exec -e CORE_PEER_ADDRESS="${host}:7051" "${host}" peer channel list 2>&1)
    echo "${host}: $(tail -1 <<<"${out}")"
    grep -qx "${channel}" <<<"${out}" || { echo "${host} did not join ${channel}" >&2; exit 1; }
  done
done
echo "network on Fabric ${fabric_tag}: orderer and 4 peers joined ${channel}"
