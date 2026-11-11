#!/usr/bin/env bash
# network-smoke.sh generates a two-org network with the builder image, brings
# it up on one Fabric release, checks that the orderer and every peer joined
# the channel, installs a CCaaS chaincode definition on the channel, and
# renews every leaf cert with reissue and restarts the nodes on them. It runs
# the builder the way a consumer does (docker run with the Docker socket and
# the project directory at the same path). It runs it as the caller's user
# with the Docker socket's group, not as root (#28).
#
# Why: the builder talks to the nodes itself (the orderer's channel
# participation API, the peers' admin and lifecycle gRPC calls). A Fabric
# release that changes that flow (Fabric 3 removed the system channel) breaks
# `up` or `install`, not `generate`, and only a real network shows it
# (luthersystems/substrate#1034).
#
# Usage: tests/network-smoke.sh IMAGE FABRIC_TAG
#   FNB_SMOKE_DOCKER_OPTS  extra `docker run` options
#   FNB_SMOKE_IMAGENS      run with this FNB_IMAGENS, and check that every
#                          node runs an image from it (#45: a consumer that
#                          pulls through a mirror must get every image there)
set -o errexit
set -o nounset
set -o pipefail

image=${1:?usage: $0 IMAGE FABRIC_TAG}
fabric_tag=${2:?usage: $0 IMAGE FABRIC_TAG}
channel=luther

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
mkdir -p "${repo}/build"
work=$(mktemp -d "${repo}/build/smoke.XXXXXX")

# The Docker socket's group as a container sees it, found the way the README
# says: the image's own stat.
sock_gid=$(docker run --rm --entrypoint stat -v /var/run/docker.sock:/var/run/docker.sock "${image}" -c %g /var/run/docker.sock)

fnb() {
  # shellcheck disable=SC2086
  docker run --rm \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "${work}:${work}" -w "${work}" \
    -e FNB_IMAGETAG="${fabric_tag}" \
    -e FNB_IMAGENS="${FNB_SMOKE_IMAGENS:-}" \
    -u "$(id -u):$(id -g)" --group-add "${sock_gid}" \
    ${FNB_SMOKE_DOCKER_OPTS:-} \
    "${image}" --chown "$(id -u):$(id -g)" --channel "${channel}" --force "$@"
}

# On a failure, print each node's state and last log lines, so a node that
# stopped can be read from the CI log.
dump_nodes() {
  local c
  for c in orderer0.luther.systems peer0.org1.luther.systems peer1.org1.luther.systems \
    peer0.org2.luther.systems peer1.org2.luther.systems; do
    echo "----- ${c}: $(docker inspect -f '{{.State.Status}} exit={{.State.ExitCode}} restarts={{.RestartCount}}' "${c}" 2>&1)" >&2
    docker logs --tail 40 "${c}" 2>&1 | sed "s/^/${c}: /" >&2 || true
  done
}

cleanup() {
  status=$?
  [ "${status}" -eq 0 ] || dump_nodes
  fnb down >/dev/null 2>&1 || true
  rm -rf "${work}"
}
trap cleanup EXIT

fnb generate --domain=luther.systems --cc-name=smoke --org-count=2 --peer-count=2
test -f "${work}/channel-artifacts/${channel}.block"
# The Docker network joins with the channel block; these are for the mars
# Kubernetes roles (fnb-gen.sh), as v0.0.10 wrote them.
for f in genesis.block channel.tx Org1MSPanchors.tx Org2MSPanchors.tx; do
  test -f "${work}/channel-artifacts/${f}"
done

fnb -s goleveldb up --cc-version smoke

running=$(docker inspect -f '{{.Config.Image}}' orderer0.luther.systems)
echo "orderer image: ${running}"
case "${running}" in
  *":${fabric_tag}") ;;
  *) echo "orderer runs ${running}, want tag ${fabric_tag}" >&2; exit 1 ;;
esac

# Docker names docker.io/hyperledger/x and hyperledger/x the same image, so
# only the reference the container was created from shows that the builder
# put FNB_IMAGENS in front of each image.
want_ns=${FNB_SMOKE_IMAGENS:-hyperledger}
for c in orderer0.luther.systems peer0.org1.luther.systems peer1.org1.luther.systems \
  peer0.org2.luther.systems peer1.org2.luther.systems; do
  running=$(docker inspect -f '{{.Config.Image}}' "${c}")
  case "${running}" in
    "${want_ns}/"*) ;;
    *) echo "${c} runs ${running}, want an image from ${want_ns}" >&2; exit 1 ;;
  esac
done

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
# A CCaaS package needs no chaincode server to install, approve and commit:
# the lifecycle runs on the peers alone.
fnb generatecc --ccaas smoke 1.0 smoke /chaincodes/smoke-1.0.tar.gz
fnb install smoke 1.0 smoke /chaincodes/smoke-1.0.tar.gz
for org in 1 2; do
  host=peer0.org${org}.luther.systems
  out=$(docker exec -e CORE_PEER_ADDRESS="${host}:7051" "${host}" \
    peer lifecycle chaincode querycommitted -C "${channel}" -n smoke 2>&1)
  echo "${host}: ${out}"
  grep -q "Version: 1.0, Sequence: 1" <<<"${out}" || { echo "smoke is not committed at sequence 1 on ${host}" >&2; exit 1; }
done

# Renew every leaf cert in place, as an operator does before the certs
# expire, then restart the nodes on the new certs. The nodes must still
# serve the channel and the committed chaincode: reissue keeps each key and
# subject, so the MSP identities do not change.
peer_tls=crypto-config/peerOrganizations/org1.luther.systems/peers/peer0.org1.luther.systems/tls/server.crt
before=$(openssl x509 -noout -serial -in "${work}/${peer_tls}")
# Read the whole output first: with pipefail, `fnb ... | grep -q` fails when
# grep exits at its match before the builder has written the rest (broken
# pipe), which made this check fail at random.
expiries=$(fnb cert_expiries)
grep -q "${peer_tls}" <<<"${expiries}" || { echo "cert_expiries does not list ${peer_tls}" >&2; exit 1; }
fnb reissue --all --days 30
after=$(openssl x509 -noout -serial -in "${work}/${peer_tls}")
[[ "${before}" != "${after}" ]] || { echo "reissue did not replace ${peer_tls}" >&2; exit 1; }
openssl x509 -noout -checkend $((31 * 86400)) -in "${work}/${peer_tls}" >/dev/null \
  && { echo "${peer_tls} is valid past 31 days after reissue --days 30" >&2; exit 1; }
docker restart orderer0.luther.systems \
  peer0.org1.luther.systems peer1.org1.luther.systems \
  peer0.org2.luther.systems peer1.org2.luther.systems >/dev/null
for org in 1 2; do
  for peer in 0 1; do
    host=peer${peer}.org${org}.luther.systems
    out=
    # A restarted peer takes a few seconds to serve again.
    for _ in $(seq 30); do
      if out=$(docker exec -e CORE_PEER_ADDRESS="${host}:7051" "${host}" \
        peer lifecycle chaincode querycommitted -C "${channel}" -n smoke 2>&1) \
        && grep -q "Version: 1.0, Sequence: 1" <<<"${out}"; then
        break
      fi
      sleep 2
    done
    echo "${host} after reissue: ${out}"
    grep -q "Version: 1.0, Sequence: 1" <<<"${out}" || { echo "${host} does not serve ${channel} after reissue" >&2; exit 1; }
  done
done
ocheck=$(docker exec cli osnadmin channel list --channelID "${channel}" \
  -o orderer0.luther.systems:7053 \
  --ca-file /crypto-config/ordererOrganizations/luther.systems/orderers/orderer0.luther.systems/msp/tlscacerts/tlsca.luther.systems-cert.pem \
  --client-cert "${admin}/client.crt" --client-key "${admin}/client.key" 2>&1)
grep -q '"status": "active"' <<<"${ocheck}" || { echo "orderer0 is not active in ${channel} after reissue: ${ocheck}" >&2; exit 1; }
echo "network on Fabric ${fabric_tag}: orderer and 4 peers joined ${channel}; smoke 1.0 committed; nodes serve on reissued certs"
