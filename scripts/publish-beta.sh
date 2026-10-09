#!/usr/bin/env bash
# Publish one platform's build to the rolling Beta release
# (docs/plans/versioning-releases-and-updates.md §3.7).
#
#   scripts/publish-beta.sh <platform> <dir>
#
# <dir> holds manifest-<platform>.json, its .sig, and every file the manifest
# lists. They replace the same-named files of the `channel-beta` prerelease,
# which holds one build at a time, so the updater (and install-macos.sh) can
# fetch Beta from a public, permanent URL with no token:
#   https://github.com/<repo>/releases/download/channel-beta/<file>
#
# Inputs, from the environment:
#   GH_TOKEN, GH_REPO   for the gh CLI; the token needs contents: write
#   COMMIT              the commit the build is from; the tag moves to it
#   STAMP               the build's stamp
#
# The order is what keeps a reader safe during an upload: payloads first, the
# manifest last. Until the manifest lands, the release's manifest still
# describes the old payloads, and a reader that fetches a new payload under it
# gets a checksum mismatch and tries again later rather than installing a
# mixed pair.

set -euo pipefail

readonly TAG=channel-beta

die() {
	echo "publish-beta.sh: $*" >&2
	exit 1
}

(($# == 2)) || die "usage: publish-beta.sh <platform> <dir>"
platform=$1 dir=$2
manifest="$dir/manifest-$platform.json"

for var in GH_REPO COMMIT STAMP; do
	[[ -n ${!var:-} ]] || die "$var is not set"
done
[[ -f $manifest ]] || die "$manifest is missing"
# Beta installs on its own, so it only ever carries signed builds.
[[ -f $manifest.sig ]] || die "$manifest is unsigned; Beta only publishes signed builds"
[[ $(jq -r .stamp "$manifest") == "$STAMP" ]] || die "$manifest is not this build's (stamp $STAMP)"

mapfile -t payloads < <(jq -r '.files[].name' "$manifest")
((${#payloads[@]} > 0)) || die "$manifest lists no files"
for f in "${payloads[@]}"; do
	[[ -f $dir/$f ]] || die "$dir/$f is listed in the manifest but missing"
done

# Created on first use. The desktop and Android workflows can both get here at
# once, so a create that loses the race is fine as long as the release exists.
if ! gh release view "$TAG" >/dev/null 2>&1; then
	gh release create "$TAG" --prerelease --target "$COMMIT" --title "Beta" \
		--notes "The newest build of the default branch, for TwoPlacePaste's Beta update channel. Its files are replaced on every push; for installable releases see the other releases." ||
		gh release view "$TAG" >/dev/null 2>&1 ||
		die "could not create the $TAG release"
fi

# A slower, older run must not overwrite a newer build that already landed.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
if gh release download "$TAG" --pattern "manifest-$platform.json" --dir "$tmp" >/dev/null 2>&1; then
	current=$(jq -r '.stamp // 0' "$tmp/manifest-$platform.json")
	if ((current >= STAMP)); then
		echo "::notice::Beta already has a $platform build at stamp $current (this is $STAMP); not publishing"
		exit 0
	fi
fi

paths=()
for f in "${payloads[@]}"; do
	paths+=("$dir/$f")
done
gh release upload "$TAG" "${paths[@]}" --clobber
gh release upload "$TAG" "$manifest" "$manifest.sig" --clobber

# The tag follows the newest build, so the release's source links match it.
gh api --method PATCH "repos/$GH_REPO/git/refs/tags/$TAG" -f sha="$COMMIT" -F force=true >/dev/null

echo "published the $platform build at stamp $STAMP to $TAG"
