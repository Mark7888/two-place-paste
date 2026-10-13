#!/usr/bin/env bash
# Tests for scripts/version.sh, each against a throwaway git repository.
#
#   scripts/version_test.sh
#
# Exits non-zero, naming the failing case, if any expectation does not hold.

set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/version.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

failures=0

# 2026-10-08T16:15:00Z, as seconds since 2025-01-01T00:00:00Z.
readonly STAMP=55786500

new_repo() {
	rm -rf "$work/repo"
	git init -q -b master "$work/repo"
	git -C "$work/repo" config user.email test@example.com
	git -C "$work/repo" config user.name test
	git -C "$work/repo" config commit.gpgsign false
	git -C "$work/repo" config tag.gpgsign false
}

commit() {
	git -C "$work/repo" commit -q --allow-empty -m "$1"
}

# run VAR=value… : run version.sh in the repo with a clean GitHub environment.
run() {
	(
		cd "$work/repo"
		env -u GITHUB_OUTPUT -u GITHUB_REF_TYPE -u GITHUB_REF_NAME -u GITHUB_EVENT_NAME \
			-u GITHUB_SHA -u TPP_HEAD_SHA -u TPP_DEFAULT_BRANCH \
			TPP_STAMP="$STAMP" "$@" bash "$script"
	)
}

# expect NAME OUTPUT KEY VALUE
expect() {
	local got
	got=$(sed -n "s/^$3=//p" <<<"$2")
	if [[ $got != "$4" ]]; then
		echo "FAIL $1: $3 = '$got', want '$4'" >&2
		failures=$((failures + 1))
	fi
}

head_sha() { git -C "$work/repo" rev-parse HEAD; }

# --- no tags at all: the base is v0.0.0 --------------------------------------
new_repo
commit first
sha=$(head_sha)
out=$(run GITHUB_REF_TYPE=branch GITHUB_REF_NAME=master GITHUB_EVENT_NAME=push)
expect no-tags "$out" version "0.0.1-dev.20261008161500+${sha:0:7}"
expect no-tags "$out" numeric 0.0.1
expect no-tags "$out" stamp "$STAMP"
expect no-tags "$out" channel beta
expect no-tags "$out" commit "$sha"
expect no-tags "$out" prerelease false

# --- one tag behind HEAD: the next patch, dev ---------------------------------
git -C "$work/repo" tag v1.2.3
commit second
sha=$(head_sha)
out=$(run GITHUB_REF_TYPE=branch GITHUB_REF_NAME=master GITHUB_EVENT_NAME=push)
expect one-tag "$out" version "1.2.4-dev.20261008161500+${sha:0:7}"
expect one-tag "$out" numeric 1.2.4

# --- the default branch is configurable (main) --------------------------------
out=$(run GITHUB_REF_TYPE=branch GITHUB_REF_NAME=main GITHUB_EVENT_NAME=push TPP_DEFAULT_BRANCH=main)
expect main-branch "$out" channel beta
out=$(run GITHUB_REF_TYPE=branch GITHUB_REF_NAME=master GITHUB_EVENT_NAME=push TPP_DEFAULT_BRANCH=main)
expect not-default "$out" channel nightly

# --- a pre-release tag is not a base ------------------------------------------
git -C "$work/repo" tag v1.3.0-rc.1
commit third
out=$(run GITHUB_REF_TYPE=branch GITHUB_REF_NAME=master GITHUB_EVENT_NAME=push)
expect rc-not-base "$out" numeric 1.2.4

# --- a newer tag on another branch is not reachable, so not the base ----------
git -C "$work/repo" checkout -q -b side
commit side
git -C "$work/repo" tag v2.0.0
git -C "$work/repo" checkout -q master
out=$(run GITHUB_REF_TYPE=branch GITHUB_REF_NAME=master GITHUB_EVENT_NAME=push)
expect other-branch-tag "$out" numeric 1.2.4

# --- a pull request is nightly and named after the PR's head commit ----------
pr_head=$(git -C "$work/repo" rev-parse side)
out=$(run GITHUB_REF_TYPE=branch GITHUB_REF_NAME=22/merge GITHUB_EVENT_NAME=pull_request TPP_HEAD_SHA="$pr_head")
expect pull-request "$out" channel nightly
expect pull-request "$out" commit "$pr_head"
expect pull-request "$out" version "1.2.4-dev.20261008161500+${pr_head:0:7}"
# Even a PR whose head branch is called master is not Beta.
out=$(run GITHUB_REF_TYPE=branch GITHUB_REF_NAME=master GITHUB_EVENT_NAME=pull_request)
expect pull-request-master "$out" channel nightly

# --- a run of the release tag itself ------------------------------------------
git -C "$work/repo" tag v1.3.0
out=$(run GITHUB_REF_TYPE=tag GITHUB_REF_NAME=v1.3.0 GITHUB_EVENT_NAME=push)
expect release "$out" version 1.3.0
expect release "$out" numeric 1.3.0
expect release "$out" channel stable
expect release "$out" prerelease false
expect release "$out" stamp "$STAMP"

# --- a pre-release tag run -----------------------------------------------------
out=$(run GITHUB_REF_TYPE=tag GITHUB_REF_NAME=v1.4.0-rc.2 GITHUB_EVENT_NAME=push)
expect rc-release "$out" version 1.4.0-rc.2
expect rc-release "$out" numeric 1.4.0
expect rc-release "$out" channel stable
expect rc-release "$out" prerelease true

# --- after the release, dev builds move on to the next patch -----------------
commit fourth
out=$(run GITHUB_REF_TYPE=branch GITHUB_REF_NAME=master GITHUB_EVENT_NAME=push)
expect after-release "$out" numeric 1.3.1

# --- outputs are appended to $GITHUB_OUTPUT -----------------------------------
: >"$work/output"
run GITHUB_REF_TYPE=tag GITHUB_REF_NAME=v1.3.0 GITHUB_OUTPUT="$work/output" >/dev/null
expect github-output "$(cat "$work/output")" version 1.3.0

# --- bad input fails loudly ----------------------------------------------------
for bad in "GITHUB_REF_TYPE=tag GITHUB_REF_NAME=release-1" \
	"GITHUB_REF_TYPE=tag GITHUB_REF_NAME=v1.2" \
	"TPP_STAMP=soon" \
	"TPP_HEAD_SHA=abc123"; do
	# shellcheck disable=SC2086 # word splitting into VAR=value pairs is the point
	if run $bad >/dev/null 2>&1; then
		echo "FAIL rejects '$bad': it succeeded" >&2
		failures=$((failures + 1))
	fi
done

# --- without TPP_STAMP the stamp is the current time --------------------------
out=$(cd "$work/repo" && env -u TPP_STAMP GITHUB_REF_TYPE=branch GITHUB_REF_NAME=master bash "$script")
now=$(($(date -u +%s) - 1735689600))
got=$(sed -n 's/^stamp=//p' <<<"$out")
if ((got < now - 60 || got > now + 60)); then
	echo "FAIL live-stamp: stamp = $got, want about $now" >&2
	failures=$((failures + 1))
fi

if ((failures > 0)); then
	echo "$failures failure(s)" >&2
	exit 1
fi
echo "version.sh: all tests passed"
