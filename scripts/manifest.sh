#!/usr/bin/env bash
# Write, and sign, the update manifest for one platform's build
# (docs/plans/versioning-releases-and-updates.md §3.6).
#
#   scripts/manifest.sh <platform> <out-dir> <file>...
#
# <platform> is desktop or android. The manifest lists every <file> by base
# name with its SHA-256 and size, next to the build's version, and is written to
# <out-dir>/manifest-<platform>.json. The updater installs a file only if its
# checksum is in a manifest whose signature verifies against the public key
# compiled into the app.
#
# Inputs, from the environment:
#   VERSION, NUMERIC, STAMP, CHANNEL, COMMIT   the outputs of scripts/version.sh
#   GITHUB_REPOSITORY, GITHUB_RUN_ID           set by Actions
#   UPDATE_SIGNING_KEY  the Ed25519 private key, PEM. When it is empty (a fork's
#                       pull request never sees secrets) the manifest is written
#                       unsigned, and the updater treats the build as unsigned.
#   TPP_SIGNING_PUBKEY  the public key to check a fresh signature against
#                       (default: the one the app embeds). A secret that holds
#                       the wrong key fails the build here, not on every
#                       user's machine.
#
# Signing needs OpenSSL 3 (pkeyutl -rawin); the Ubuntu runners have it.

set -euo pipefail

die() {
	echo "manifest.sh: $*" >&2
	exit 1
}

(($# >= 3)) || die "usage: manifest.sh <platform> <out-dir> <file>..."
platform=$1 out_dir=$2
shift 2
[[ $platform == desktop || $platform == android ]] || die "unknown platform '$platform'"

for var in VERSION NUMERIC STAMP CHANNEL COMMIT; do
	[[ -n ${!var:-} ]] || die "$var is not set"
done
[[ $STAMP =~ ^[0-9]+$ ]] || die "STAMP '$STAMP' is not a number"

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pubkey=${TPP_SIGNING_PUBKEY:-$root/desktop/internal/update/update-signing.pub.pem}

sha256() {
	if command -v sha256sum >/dev/null; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

files='[]'
for f in "$@"; do
	[[ -f $f ]] || die "$f is not a file"
	files=$(jq -c \
		--arg name "$(basename "$f")" \
		--arg sha256 "$(sha256 "$f")" \
		--argjson size "$(wc -c <"$f" | tr -d ' ')" \
		'. + [{name: $name, sha256: $sha256, size: $size}]' <<<"$files")
done

mkdir -p "$out_dir"
manifest="$out_dir/manifest-$platform.json"
jq -n \
	--arg platform "$platform" \
	--arg repository "${GITHUB_REPOSITORY:-}" \
	--arg version "$VERSION" \
	--arg numeric "$NUMERIC" \
	--argjson stamp "$STAMP" \
	--arg channel "$CHANNEL" \
	--arg commit "$COMMIT" \
	--arg run_id "${GITHUB_RUN_ID:-}" \
	--argjson files "$files" \
	'{schema: 1, platform: $platform, repository: $repository, version: $version,
	  numeric: $numeric, stamp: $stamp, channel: $channel, commit: $commit,
	  run_id: $run_id, files: $files}' >"$manifest"

if [[ -z ${UPDATE_SIGNING_KEY:-} ]]; then
	rm -f "$manifest.sig"
	echo "::notice::no update-signing key available; $manifest is unsigned"
	exit 0
fi

# The key only ever touches a private temporary file, never an argument list.
key=$(umask 077 && mktemp)
trap 'rm -f "$key"' EXIT
printf '%s\n' "$UPDATE_SIGNING_KEY" >"$key"

# Ed25519 signs the message itself, not a digest of it: hence -rawin.
openssl pkeyutl -sign -rawin -inkey "$key" -in "$manifest" -out "$manifest.sig" ||
	die "signing failed: is UPDATE_SIGNING_KEY an Ed25519 private key in PEM form?"
openssl pkeyutl -verify -rawin -pubin -inkey "$pubkey" -in "$manifest" -sigfile "$manifest.sig" >/dev/null ||
	die "the signature does not verify against $pubkey: UPDATE_SIGNING_KEY is not the key the app trusts"

echo "signed $manifest"
