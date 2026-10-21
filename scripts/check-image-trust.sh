#!/usr/bin/env bash
# Check that an image has no eMudhra (emSign) root CA (CVE-2026-111943) in
# its trust stores, and that the image still starts.
#
# Usage: scripts/check-image-trust.sh IMAGE [PLATFORM]
#
# The check does not trust the image's own tools: it copies each bundle out
# of the image and reads each certificate with the host's openssl. It checks
#   - the system bundle /etc/ssl/certs/ca-certificates.crt, which must hold
#     at least 100 roots (fewer means the store is broken, not clean);
#   - each certificate file in /etc/ssl/certs (OpenSSL's hash directory,
#     which Python's ssl module and curl read);
#   - each cacert.pem in the image (certifi, and pip's vendored certifi).
# A subject that contains "emsign" (any case) fails the check.
# Then it runs the entrypoint with --help.
set -euo pipefail

image=${1:?usage: $0 IMAGE [PLATFORM]}
platform=${2:-}
plat_args=()
if [ -n "$platform" ]; then
	plat_args=(--platform "$platform")
fi

work=$(mktemp -d)
cid=$(docker create "${plat_args[@]}" "$image")
cleanup() {
	docker rm -f "$cid" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

fail=0

# check_bundle NAME FILE MIN: print the number of certificates in FILE, and
# fail if one has an emSign subject or if there are fewer than MIN.
check_bundle() {
	local name=$1 file=$2 min=$3 dir n bad
	dir=$(mktemp -d -p "$work")
	awk -v dir="$dir" '
		/^-----BEGIN CERTIFICATE-----/ { n++; out = sprintf("%s/%05d.pem", dir, n); inblk = 1 }
		inblk { print > out }
		/^-----END CERTIFICATE-----/ { inblk = 0; close(out) }
	' "$file"
	n=0
	bad=0
	for pem in "$dir"/*.pem; do
		[ -e "$pem" ] || continue
		n=$((n + 1))
		# A block openssl cannot read is a failure: it must not pass unseen.
		if ! subject=$(openssl x509 -noout -subject -in "$pem" 2>&1); then
			echo "FAIL: $name: openssl cannot read certificate $n: $subject" >&2
			bad=$((bad + 1))
			continue
		fi
		if grep -qi 'emsign' <<<"$subject"; then
			echo "FAIL: $name: $subject" >&2
			bad=$((bad + 1))
		fi
	done
	echo "$name: $n certificate(s), $bad bad"
	if [ "$bad" -gt 0 ]; then
		fail=1
	fi
	if [ "$n" -lt "$min" ]; then
		echo "FAIL: $name: $n certificate(s), expected at least $min" >&2
		fail=1
	fi
}

docker cp -L "$cid:/etc/ssl/certs/ca-certificates.crt" "$work/system.crt"
check_bundle /etc/ssl/certs/ca-certificates.crt "$work/system.crt" 100

# Copy the trust store files out of the image. The checks below read this
# copy, so no tool in the image takes part. The image is streamed twice (a
# list, then an extract) so its full size never lands on disk.
mkdir "$work/root"
docker export "$cid" | tar -tf - | grep -E '(^|/)cacert\.pem$' >"$work/pems" || true
docker export "$cid" | tar -xf - -C "$work/root" --wildcards \
	'etc/ssl/certs*' 'usr/share/ca-certificates*' 'usr/local/share/ca-certificates*' \
	$(cat "$work/pems")

# resolve PATH: print the file that PATH (a path in the image) points to,
# following links inside the copy of the image, not on the host.
resolve() {
	local p=$1 t i
	for i in $(seq 1 20); do
		if [ ! -L "$work/root$p" ]; then
			echo "$work/root$p"
			return
		fi
		t=$(readlink "$work/root$p")
		case $t in
		/*) p=$t ;;
		*) p=$(dirname "$p")/$t ;;
		esac
	done
	echo "$work/root$p"
}

# OpenSSL's hash directory: each *.pem and each hash link (*.0, *.1, ...).
: >"$work/certdir.crt"
for f in "$work"/root/etc/ssl/certs/*; do
	name=${f#"$work/root"}
	case $name in
	*.pem | *.[0-9]) ;;
	*) continue ;;
	esac
	target=$(resolve "$name")
	if [ ! -f "$target" ]; then
		# A dangling link adds no root; OpenSSL skips it too.
		continue
	fi
	cat "$target" >>"$work/certdir.crt"
done
check_bundle "/etc/ssl/certs/*.pem and hash links" "$work/certdir.crt" 100

if [ ! -s "$work/pems" ]; then
	echo "no cacert.pem in the image"
fi
while IFS= read -r path; do
	check_bundle "/$path" "$(resolve "/$path")" 1
done <"$work/pems"

# The image must still start.
docker run --rm "${plat_args[@]}" "$image" --help >"$work/help" 2>&1 || {
	cat "$work/help" >&2
	echo "FAIL: '$image --help' failed" >&2
	fail=1
}
head -5 "$work/help"

if [ "$fail" -ne 0 ]; then
	echo "FAIL: $image" >&2
	exit 1
fi
echo "OK: $image has no emSign root"
