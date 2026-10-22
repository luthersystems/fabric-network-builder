#!/usr/bin/env bash
# Check the Dockerfile and a built image with the build-harden kit's checks,
# and check that the image still starts.
#
# Usage: scripts/check-image-harden.sh IMAGE [PLATFORM]
#
# The kit is copied out of the image that the Dockerfile pins, so CI checks
# with the same kit version that the build ran. The checks:
#   - check/dockerfile.sh: the final stage runs the kit last, from a stage
#     pinned by digest, and no later step can undo it;
#   - check/image.sh: on the exported image, with the host's openssl, no
#     trust store (system bundle, OpenSSL hash directory, certifi) holds or
#     chains to a distrusted CA, and the system bundle is not empty.
# Then it runs the entrypoint with --help.
set -euo pipefail

image=${1:?usage: $0 IMAGE [PLATFORM]}
platform=${2:-}
plat_args=()
if [ -n "$platform" ]; then
	plat_args=(--platform "$platform")
fi

cd "$(dirname "$0")/.."

kit=$(sed -n 's/^FROM \(luthersystems\/build-harden[^ ]*\) AS harden$/\1/p' Dockerfile)
if [ -z "$kit" ]; then
	echo "FAIL: Dockerfile has no 'FROM luthersystems/build-harden... AS harden' line" >&2
	exit 1
fi

work=$(mktemp -d)
cid=
cleanup() {
	[ -z "$cid" ] || docker rm -f "$cid" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

cid=$(docker create "$kit" /nonexistent)
docker cp "$cid:/harden" "$work/harden"
docker rm "$cid" >/dev/null
cid=

bash "$work/harden/check/dockerfile.sh" Dockerfile
bash "$work/harden/check/image.sh" "${plat_args[@]}" "$image"

# The image must still start.
docker run --rm "${plat_args[@]}" "$image" --help >"$work/help" 2>&1 || {
	cat "$work/help" >&2
	echo "FAIL: '$image --help' failed" >&2
	exit 1
}
head -5 "$work/help"
echo "OK: $image"
