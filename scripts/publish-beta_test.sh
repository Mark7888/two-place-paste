#!/usr/bin/env bash
# Tests for scripts/publish-beta.sh, against a fake gh that records its calls
# and keeps the "release" in a directory.
#
#   scripts/publish-beta_test.sh

set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/publish-beta.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

failures=0
fail() {
	echo "FAIL $*" >&2
	failures=$((failures + 1))
}

# --- the fake gh --------------------------------------------------------------------
mkdir -p "$work/bin"
cat >"$work/bin/gh" <<'EOF'
#!/usr/bin/env bash
# Records every call; the release's files live in $FAKE_RELEASE.
echo "$*" >>"$FAKE_LOG"
case "$1 $2" in
"release view") [[ -d $FAKE_RELEASE ]] ;;
"release create") mkdir -p "$FAKE_RELEASE" ;;
"release download")
	pattern=$5 dir=$7
	[[ -f $FAKE_RELEASE/$pattern ]] && cp "$FAKE_RELEASE/$pattern" "$dir/"
	;;
"release upload")
	shift 3
	for a in "$@"; do [[ $a == --clobber ]] || cp "$a" "$FAKE_RELEASE/"; done
	;;
"api --method") ;;
*)
	echo "fake gh: unexpected call: $*" >&2
	exit 1
	;;
esac
EOF
chmod +x "$work/bin/gh"

export PATH="$work/bin:$PATH" FAKE_LOG="$work/log" FAKE_RELEASE="$work/release"
export GH_REPO=Mark7888/two-place-paste COMMIT=0123456789abcdef0123456789abcdef01234567

# build STAMP: a signed-looking desktop build in $work/build-STAMP
build() {
	local d="$work/build-$1"
	mkdir -p "$d"
	printf 'mac %s' "$1" >"$d/TwoPlacePaste-macOS-arm64.zip"
	printf 'win %s' "$1" >"$d/TwoPlacePaste-Windows-x64.exe"
	jq -n --argjson stamp "$1" '{schema: 1, stamp: $stamp, files: [
		{name: "TwoPlacePaste-macOS-arm64.zip"}, {name: "TwoPlacePaste-Windows-x64.exe"}]}' \
		>"$d/manifest-desktop.json"
	printf 'sig' >"$d/manifest-desktop.json.sig"
	echo "$d"
}

# --- first publish creates the release; payloads go up before the manifest -----
d=$(build 100)
STAMP=100 bash "$script" desktop "$d" >/dev/null
grep -q '^release create channel-beta --prerelease' "$FAKE_LOG" || fail "first: release not created"
[[ $(cat "$FAKE_RELEASE/TwoPlacePaste-macOS-arm64.zip") == "mac 100" ]] || fail "first: payload not uploaded"
[[ $(jq .stamp "$FAKE_RELEASE/manifest-desktop.json") == 100 ]] || fail "first: manifest not uploaded"
order=$(grep -n '^release upload' "$FAKE_LOG")
payload_line=$(grep 'TwoPlacePaste-macOS-arm64.zip' <<<"$order" | cut -d: -f1)
manifest_line=$(grep 'manifest-desktop.json ' <<<"$order" | cut -d: -f1)
((payload_line < manifest_line)) || fail "first: the manifest was uploaded before the payloads"
grep -q "^api --method PATCH repos/$GH_REPO/git/refs/tags/channel-beta -f sha=$COMMIT -F force=true" "$FAKE_LOG" ||
	fail "first: tag not moved"

# --- a newer build replaces it, without creating the release again ------------
: >"$FAKE_LOG"
d=$(build 200)
STAMP=200 bash "$script" desktop "$d" >/dev/null
grep -q '^release create' "$FAKE_LOG" && fail "newer: created the release again"
[[ $(jq .stamp "$FAKE_RELEASE/manifest-desktop.json") == 200 ]] || fail "newer: not published"

# --- an older, slower run does not overwrite it --------------------------------
: >"$FAKE_LOG"
d=$(build 150)
STAMP=150 bash "$script" desktop "$d" >/dev/null
grep -q '^release upload' "$FAKE_LOG" && fail "older: uploaded over a newer build"
[[ $(jq .stamp "$FAKE_RELEASE/manifest-desktop.json") == 200 ]] || fail "older: replaced the newer manifest"

# --- the same stamp twice (a re-run of a published job) is a no-op ------------
: >"$FAKE_LOG"
d=$(build 200)
STAMP=200 bash "$script" desktop "$d" >/dev/null
grep -q '^release upload' "$FAKE_LOG" && fail "same: uploaded again"

# --- refusals -----------------------------------------------------------------------
d=$(build 300)
rm "$d/manifest-desktop.json.sig"
STAMP=300 bash "$script" desktop "$d" >/dev/null 2>&1 && fail "unsigned: published"

d=$(build 400)
rm "$d/TwoPlacePaste-Windows-x64.exe"
STAMP=400 bash "$script" desktop "$d" >/dev/null 2>&1 && fail "missing payload: published"

d=$(build 500)
STAMP=501 bash "$script" desktop "$d" >/dev/null 2>&1 && fail "stamp mismatch: published"

[[ $(jq .stamp "$FAKE_RELEASE/manifest-desktop.json") == 200 ]] || fail "refusals: something was published"

if ((failures > 0)); then
	echo "$failures failure(s)" >&2
	exit 1
fi
echo "publish-beta.sh: all tests passed"
