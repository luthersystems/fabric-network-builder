#!/usr/bin/env bash
# fabric-oracle.sh compares what the builder makes with what the Fabric
# release tools (cryptogen, configtxgen, configtxlator) make from the same
# input. It needs no Docker daemon unless FNB runs the builder image.
#
# Why: the builder runs cryptogen and configtxgen in process, from a copy of
# Fabric's code (third_party/fabric) that imports Fabric's public packages
# from fabric-x-common. A difference between those packages and Fabric's
# changes the crypto or the channel genesis block of every new network. One
# did: fabric-x-common hashes block data the Fabric-X way, and a Fabric 3.1
# orderer refused the block (#34). This test finds such a difference before
# a customer network does.
#
# Checks:
#   - The channel genesis block decodes to the same JSON as configtxgen's,
#     except the nonce, tx ID, timestamp and data hash.
#   - So do the v0.0.10 files that fnb-gen.sh ships to the mars Kubernetes
#     roles: the system channel genesis.block, channel.tx and the
#     <MSP>anchors.tx updates (the last against Fabric 2.5.16).
#   - cryptogen makes the same files, the same config.yaml files, and certs
#     with the same subject, issuer, extensions and validity length.
#   - `extend` on that network matches `cryptogen extend`: it adds no file
#     and changes no existing key or cert.
#
# Usage: tests/fabric-oracle.sh FABRIC_VERSION BUILDER
#   FABRIC_VERSION  a Fabric release, for example 3.1.4
#   BUILDER         the builder binary, or else the builder image to run
#   FABRIC_BIN      a directory with the release tools; by default the script
#                   downloads them to build/fabric-<version>
#   FABRIC_BIN_25   the same for Fabric 2.5.16, whose configtxgen still
#                   writes anchor peer updates (Fabric 3 removed the flag)
set -o errexit
set -o nounset
set -o pipefail

version=${1:?usage: $0 FABRIC_VERSION BUILDER}
builder=${2:?usage: $0 FABRIC_VERSION BUILDER}
channel=luther
profile=AnyOrgsApplicationGenesis

repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
mkdir -p "${repo}/build"

# tools VERSION: prints the directory with the release tools of VERSION.
tools() {
  local dir=${repo}/build/fabric-$1/bin
  if [ -n "${FABRIC_BIN:-}" ] && [ "$1" = "${version}" ]; then
    dir=${FABRIC_BIN}
  elif [ -n "${FABRIC_BIN_25:-}" ] && [ "$1" = "${legacy_version}" ]; then
    dir=${FABRIC_BIN_25}
  fi
  if [ ! -x "${dir}/configtxgen" ]; then
    case "$(uname -m)" in
      x86_64) arch=amd64 ;;
      aarch64 | arm64) arch=arm64 ;;
      *) echo "no Fabric release for $(uname -m)" >&2; exit 1 ;;
    esac
    mkdir -p "${repo}/build/fabric-$1"
    curl -sSfL "https://github.com/hyperledger/fabric/releases/download/v$1/hyperledger-fabric-linux-${arch}-$1.tar.gz" |
      tar -xz -C "${repo}/build/fabric-$1" bin/configtxgen bin/cryptogen bin/configtxlator
  fi
  echo "${dir}"
}
# The release whose configtxgen still writes anchor peer updates.
legacy_version=2.5.16
bin=$(tools "${version}")
bin25=$(tools "${legacy_version}")

work=$(mktemp -d "${repo}/build/oracle.XXXXXX")
trap 'rm -rf "${work}"' EXIT
net=${work}/net
mkdir -p "${net}"

fnb() {
  if [ -x "${builder}" ]; then
    (cd "${net}" && "${builder}" --channel "${channel}" --force "$@")
  else
    docker run --rm -u "$(id -u):$(id -g)" -v "${net}:${net}" -w "${net}" \
      "${builder}" --channel "${channel}" --force "$@"
  fi
}

fail=0
check() { # check NAME FILE_A FILE_B
  if diff -u "$2" "$3" >"${work}/diff"; then
    echo "ok: $1"
  else
    echo "FAIL: $1 (builder vs Fabric ${version})" >&2
    head -60 "${work}/diff" >&2
    fail=1
  fi
}

fnb generate --domain=luther.systems --cc-name=oracle --org-count=2 --peer-count=2 >/dev/null

# --- Genesis block --------------------------------------------------------
# Same configtx.yaml, same crypto: only the random and time fields may differ.
"${bin}/configtxgen" -configPath "${net}" -profile "${profile}" -channelID "${channel}" \
  -outputBlock "${work}/fabric.block" 2>/dev/null
mask='del(.header.data_hash)
  | .data.data[].payload.header.channel_header |= (del(.timestamp) | del(.tx_id))
  | .data.data[].payload.header.signature_header |= del(.nonce)'
for b in builder fabric; do
  f=${work}/fabric.block
  [ "${b}" = builder ] && f=${net}/channel-artifacts/${channel}.block
  "${bin}/configtxlator" proto_decode --type common.Block --input "${f}" |
    jq -S "${mask}" >"${work}/${b}.block.json"
done
check "genesis block config" "${work}/builder.block.json" "${work}/fabric.block.json"

# Fabric's block data hash is the SHA-256 of the joined data items. A
# genesis block with another hash is refused by a Fabric 3 orderer (#34).
block_hashes() { # block_hashes FILE: prints "<header data hash> <Fabric hash>"
  python3 -I - "$1" <<'PY'
import hashlib, sys

def fields(b):
    i = 0
    while i < len(b):
        key = n = s = 0
        while True:
            c = b[i]; i += 1; key |= (c & 0x7F) << s; s += 7
            if c < 0x80: break
        if key & 7 == 0:
            while b[i] & 0x80: i += 1
            i += 1
            continue
        if key & 7 != 2: sys.exit("unexpected wire type")
        s = 0
        while True:
            c = b[i]; i += 1; n |= (c & 0x7F) << s; s += 7
            if c < 0x80: break
        yield key >> 3, b[i:i + n]; i += n

block = dict(fields(open(sys.argv[1], "rb").read()))
header = dict(fields(block[1]))
data = [v for k, v in fields(block[2]) if k == 1]
print(header.get(3, b"").hex(), hashlib.sha256(b"".join(data)).hexdigest())
PY
}
for f in "${net}/channel-artifacts/${channel}.block" "${work}/fabric.block"; do
  read -r got want < <(block_hashes "${f}")
  echo "${got}" >"${work}/got"
  echo "${want}" >"${work}/want"
  check "data hash of $(basename "${f}")" "${work}/got" "${work}/want"
done

# --- System channel flow (v0.0.10 files for the mars Kubernetes roles) ----
envmask='.payload.header.channel_header |= (del(.timestamp) | del(.tx_id))
  | .payload.header.signature_header |= del(.nonce)'
decode() { # decode TYPE FILE MASK OUT
  "${bin}/configtxlator" proto_decode --type "$1" --input "$2" | jq -S "$3" >"$4"
}
"${bin}/configtxgen" -configPath "${net}" -profile AnyOrgsOrdererGenesis -channelID byfn-sys-channel \
  -outputBlock "${work}/fabric.genesis.block" 2>/dev/null
decode common.Block "${net}/channel-artifacts/genesis.block" "${mask}" "${work}/builder.genesis.json"
decode common.Block "${work}/fabric.genesis.block" "${mask}" "${work}/fabric.genesis.json"
check "system channel genesis block" "${work}/builder.genesis.json" "${work}/fabric.genesis.json"
read -r got want < <(block_hashes "${net}/channel-artifacts/genesis.block")
echo "${got}" >"${work}/got"
echo "${want}" >"${work}/want"
check "data hash of genesis.block" "${work}/got" "${work}/want"

"${bin}/configtxgen" -configPath "${net}" -profile AnyOrgsChannel -channelID "${channel}" \
  -outputCreateChannelTx "${work}/fabric.channel.tx" 2>/dev/null
decode common.Envelope "${net}/channel-artifacts/channel.tx" "${envmask}" "${work}/builder.channel.json"
decode common.Envelope "${work}/fabric.channel.tx" "${envmask}" "${work}/fabric.channel.json"
check "channel.tx" "${work}/builder.channel.json" "${work}/fabric.channel.json"

for org in Org1MSP Org2MSP; do
  "${bin25}/configtxgen" -configPath "${net}" -profile AnyOrgsChannel -channelID "${channel}" \
    -outputAnchorPeersUpdate "${work}/fabric.${org}anchors.tx" -asOrg "${org}" 2>/dev/null
  decode common.Envelope "${net}/channel-artifacts/${org}anchors.tx" "${envmask}" "${work}/builder.${org}.json"
  decode common.Envelope "${work}/fabric.${org}anchors.tx" "${envmask}" "${work}/fabric.${org}.json"
  check "${org}anchors.tx (Fabric ${legacy_version})" "${work}/builder.${org}.json" "${work}/fabric.${org}.json"
done

# --- Crypto material ------------------------------------------------------
"${bin}/cryptogen" generate --config "${net}/crypto-config.yaml" --output "${work}/fabric-crypto" >/dev/null

# describe DIR: one line per file, with what must match for a cert. Keys,
# serials and key identifiers are random, so they are left out.
describe() {
  (cd "$1" && find . -type f | LC_ALL=C sort | while read -r f; do
    case "${f}" in
      *.pem | *.crt)
        if openssl x509 -in "${f}" -noout 2>/dev/null; then
          start=$(date -d "$(openssl x509 -in "${f}" -noout -startdate | cut -d= -f2)" +%s)
          end=$(date -d "$(openssl x509 -in "${f}" -noout -enddate | cut -d= -f2)" +%s)
          printf '%s cert days=%s %s\n' "${f}" $(((end - start) / 86400)) \
            "$(openssl x509 -in "${f}" -noout -subject -issuer -nameopt RFC2253 \
              -ext basicConstraints,keyUsage,extendedKeyUsage,subjectAltName 2>/dev/null |
              grep -v '^X509v3\|^No extensions' | sed 's/^ *//' | tr '\n' '|')"
          printf '%s sigalg %s\n' "${f}" "$(openssl x509 -in "${f}" -noout -text |
            awk '/Signature Algorithm/ {print $3; exit}')"
        else
          printf '%s other\n' "${f}"
        fi
        ;;
      *config.yaml) printf '%s yaml %s\n' "${f}" "$(sha256sum <"${f}" | cut -c1-16)" ;;
      *) printf '%s file\n' "${f}" ;;
    esac
  done)
}
describe "${net}/crypto-config" >"${work}/builder.crypto.txt"
describe "${work}/fabric-crypto" >"${work}/fabric.crypto.txt"
grep -q ' cert days=[1-9]' "${work}/builder.crypto.txt" || { echo "FAIL: no certs read" >&2; fail=1; }
check "crypto material" "${work}/builder.crypto.txt" "${work}/fabric.crypto.txt"

# --- Extend ---------------------------------------------------------------
cp -a "${net}/crypto-config" "${work}/fabric-extend"
"${bin}/cryptogen" extend --config "${net}/crypto-config.yaml" --input "${work}/fabric-extend" >/dev/null
(cd "${net}/crypto-config" && find . -type f -exec sha256sum {} + | LC_ALL=C sort -k2) >"${work}/before.txt"
fnb extend --domain-name=luther.systems >/dev/null
(cd "${net}/crypto-config" && find . -type f -exec sha256sum {} + | LC_ALL=C sort -k2) >"${work}/after.txt"
(cd "${work}/fabric-extend" && find . -type f -exec sha256sum {} + | LC_ALL=C sort -k2) >"${work}/fabric-after.txt"
check "extend keeps every file" "${work}/after.txt" "${work}/before.txt"
check "cryptogen extend keeps every file" "${work}/fabric-after.txt" "${work}/before.txt"

exit "${fail}"
