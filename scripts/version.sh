#!/usr/bin/env bash
# Compute the version of the build a CI run is producing
# (docs/plans/versioning-releases-and-updates.md §1).
#
# The newest vX.Y.Z tag reachable from the commit is the source of truth.
# Nothing is ever written back to the repository, so two runs can never race
# each other over a version file.
#
#   tag push vX.Y.Z[-pre]  → version X.Y.Z[-pre], channel stable
#   anything else          → version X.Y.(Z+1)-dev.<UTC timestamp>+<sha7>,
#                            channel beta on the default branch, nightly elsewhere
#
# Every build also gets a stamp: whole seconds since 2025-01-01T00:00:00Z when
# the run built. It is the one ordering key ("newer" means a larger stamp) and
# doubles as Android's versionCode, which it fits until 2091.
#
# Inputs, all from the environment. The GITHUB_* ones are what Actions sets.
#   GITHUB_REF_TYPE, GITHUB_REF_NAME, GITHUB_EVENT_NAME, GITHUB_SHA
#   TPP_DEFAULT_BRANCH  the repository's default branch (default: master)
#   TPP_HEAD_SHA        the commit to name the build after, when it is not
#                       GITHUB_SHA: a pull_request run checks out a merge commit,
#                       but the updater looks builds up by the PR's head commit
#   TPP_STAMP           a stamp to use instead of the current time, so that every
#                       workflow a release calls produces the same version
#
# Outputs, as key=value lines on stdout and appended to $GITHUB_OUTPUT when set:
#   version, numeric (X.Y.Z), stamp, channel, commit, prerelease (true/false)
#
# Needs the commit's history and tags: actions/checkout with fetch-depth: 0.

set -euo pipefail

# 2025-01-01T00:00:00Z.
readonly STAMP_EPOCH=1735689600

# vX.Y.Z with an optional SemVer pre-release suffix (v1.2.0-rc.1).
readonly TAG_RE='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'

die() {
	echo "version.sh: $*" >&2
	exit 1
}

# utc_format SECONDS FORMAT: GNU date and BSD (macOS) date spell this differently.
utc_format() {
	date -u -d "@$1" "+$2" 2>/dev/null || date -u -r "$1" "+$2"
}

ref_type=${GITHUB_REF_TYPE:-branch}
ref_name=${GITHUB_REF_NAME:-$(git rev-parse --abbrev-ref HEAD)}
event=${GITHUB_EVENT_NAME:-push}
default_branch=${TPP_DEFAULT_BRANCH:-master}

commit=${TPP_HEAD_SHA:-${GITHUB_SHA:-$(git rev-parse HEAD)}}
[[ $commit =~ ^[0-9a-f]{40}$ ]] || die "commit '$commit' is not a full SHA-1"

if [[ -n ${TPP_STAMP:-} ]]; then
	[[ $TPP_STAMP =~ ^[1-9][0-9]*$ ]] || die "TPP_STAMP '$TPP_STAMP' is not a positive integer"
	stamp=$TPP_STAMP
else
	stamp=$(($(date -u +%s) - STAMP_EPOCH))
fi

prerelease=false
if [[ $ref_type == tag ]]; then
	[[ $ref_name =~ $TAG_RE ]] ||
		die "tag '$ref_name' is not a release tag (vX.Y.Z or vX.Y.Z-pre)"
	numeric="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.${BASH_REMATCH[3]}"
	version=${ref_name#v}
	[[ -n ${BASH_REMATCH[4]} ]] && prerelease=true
	channel=stable
else
	# Pre-release tags are excluded: after v1.1.0-rc.1 the base is still the
	# last real release, so dev builds read 1.0.1-dev…, not 1.1.1-dev….
	base=$(git describe --tags --abbrev=0 \
		--match 'v[0-9]*.[0-9]*.[0-9]*' --exclude 'v*-*' HEAD 2>/dev/null || true)
	if [[ -z $base ]]; then
		base=v0.0.0
	fi
	[[ $base =~ $TAG_RE ]] || die "nearest tag '$base' is not a release tag"
	major=${BASH_REMATCH[1]} minor=${BASH_REMATCH[2]} patch=${BASH_REMATCH[3]}
	numeric="$major.$minor.$((patch + 1))"
	version="$numeric-dev.$(utc_format $((stamp + STAMP_EPOCH)) %Y%m%d%H%M%S)+${commit:0:7}"

	if [[ $event != pull_request && $ref_type == branch && $ref_name == "$default_branch" ]]; then
		channel=beta
	else
		channel=nightly
	fi
fi

out="version=$version
numeric=$numeric
stamp=$stamp
channel=$channel
commit=$commit
prerelease=$prerelease"

echo "$out"
if [[ -n ${GITHUB_OUTPUT:-} ]]; then
	echo "$out" >>"$GITHUB_OUTPUT"
fi
