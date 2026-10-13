#!/usr/bin/env bash
# Tests for scripts/manifest.sh, with throwaway keys.
#
#   scripts/manifest_test.sh

set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/manifest.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

failures=0
fail() {
	echo "FAIL $*" >&2
	failures=$((failures + 1))
}

openssl genpkey -algorithm ed25519 -out "$work/key.pem" 2>/dev/null
openssl pkey -in "$work/key.pem" -pubout -out "$work/pub.pem"
openssl genpkey -algorithm ed25519 -out "$work/other.pem" 2>/dev/null
openssl pkey -in "$work/other.pem" -pubout -out "$work/other.pub.pem"

mkdir "$work/dist"
printf 'mac build' >"$work/dist/TwoPlacePaste-macOS-arm64.zip"
printf 'windows build!' >"$work/dist/TwoPlacePaste-Windows-x64.exe"

export VERSION=1.2.4-dev.20261008161500+abc1234 NUMERIC=1.2.4 STAMP=55786500 \
	CHANNEL=beta COMMIT=0123456789abcdef0123456789abcdef01234567 \
	GITHUB_REPOSITORY=Mark7888/two-place-paste GITHUB_RUN_ID=42

# --- signed --------------------------------------------------------------------
UPDATE_SIGNING_KEY=$(cat "$work/key.pem") TPP_SIGNING_PUBKEY="$work/pub.pem" \
	bash "$script" desktop "$work/out" "$work"/dist/* >/dev/null
m="$work/out/manifest-desktop.json"

[[ $(jq -r .version "$m") == "$VERSION" ]] || fail "signed: version"
[[ $(jq -r .stamp "$m") == 55786500 ]] || fail "signed: stamp"
[[ $(jq -r '.stamp | type' "$m") == number ]] || fail "signed: stamp is not a number"
[[ $(jq -r .channel "$m") == beta ]] || fail "signed: channel"
[[ $(jq -r .platform "$m") == desktop ]] || fail "signed: platform"
[[ $(jq -r .repository "$m") == Mark7888/two-place-paste ]] || fail "signed: repository"
[[ $(jq -r '.files | length' "$m") == 2 ]] || fail "signed: file count"
want=$(printf 'mac build' | sha256sum | cut -d' ' -f1)
[[ $(jq -r '.files[] | select(.name == "TwoPlacePaste-macOS-arm64.zip") | .sha256' "$m") == "$want" ]] ||
	fail "signed: sha256"
[[ $(jq -r '.files[] | select(.name == "TwoPlacePaste-Windows-x64.exe") | .size' "$m") == 14 ]] ||
	fail "signed: size"

[[ -f $m.sig ]] || fail "signed: no .sig"
[[ $(wc -c <"$m.sig" | tr -d ' ') == 64 ]] || fail "signed: an Ed25519 signature is 64 bytes"
openssl pkeyutl -verify -rawin -pubin -inkey "$work/pub.pem" -in "$m" -sigfile "$m.sig" >/dev/null ||
	fail "signed: signature does not verify"

# A changed manifest no longer verifies.
sed 's/beta/stable/' "$m" >"$work/tampered.json"
if openssl pkeyutl -verify -rawin -pubin -inkey "$work/pub.pem" -in "$work/tampered.json" \
	-sigfile "$m.sig" >/dev/null 2>&1; then
	fail "tampered: still verifies"
fi

# --- the wrong key for the app fails the build --------------------------------
if UPDATE_SIGNING_KEY=$(cat "$work/key.pem") TPP_SIGNING_PUBKEY="$work/other.pub.pem" \
	bash "$script" desktop "$work/wrong" "$work"/dist/* >/dev/null 2>&1; then
	fail "wrong-key: succeeded"
fi

# --- no key: an unsigned manifest, and no stale signature --------------------
cp "$m.sig" "$work/out/manifest-android.json.sig"
UPDATE_SIGNING_KEY='' bash "$script" android "$work/out" "$work/dist/TwoPlacePaste-Windows-x64.exe" >/dev/null
[[ -f $work/out/manifest-android.json ]] || fail "unsigned: no manifest"
[[ ! -e $work/out/manifest-android.json.sig ]] || fail "unsigned: a stale .sig was left behind"

# --- bad input -------------------------------------------------------------------
if bash "$script" ios "$work/out" "$work/dist/TwoPlacePaste-Windows-x64.exe" >/dev/null 2>&1; then
	fail "bad-platform: succeeded"
fi
if bash "$script" desktop "$work/out" "$work/dist/missing.zip" >/dev/null 2>&1; then
	fail "missing-file: succeeded"
fi
if STAMP='' bash "$script" desktop "$work/out" "$work/dist/TwoPlacePaste-Windows-x64.exe" >/dev/null 2>&1; then
	fail "no-stamp: succeeded"
fi

if ((failures > 0)); then
	echo "$failures failure(s)" >&2
	exit 1
fi
echo "manifest.sh: all tests passed"
