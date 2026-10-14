#!/usr/bin/env bash
# Tests for scripts/ghcr-cleanup.sh, against a fake gh that serves a package's
# versions from a fixture and records deletions, and a fake docker that serves
# each index's child digests.
#
#   scripts/ghcr-cleanup_test.sh

set -euo pipefail

script="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ghcr-cleanup.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

failures=0
fail() {
	echo "FAIL $*" >&2
	failures=$((failures + 1))
}

# --- the fakes ------------------------------------------------------------------------
mkdir -p "$work/bin"
cat >"$work/bin/gh" <<'EOF'
#!/usr/bin/env bash
echo "$*" >>"$FAKE_LOG"
case "$1 $2" in
"api users/"*) echo "${FAKE_OWNER_TYPE:-User}" ;;
"api --paginate")
	# Two pages, as gh --paginate prints them: one JSON array after another.
	jq '.[:2]' "$FAKE_VERSIONS"
	jq '.[2:]' "$FAKE_VERSIONS"
	;;
"api --method") ;;
*)
	echo "fake gh: unexpected call: $*" >&2
	exit 1
	;;
esac
EOF
cat >"$work/bin/docker" <<'EOF'
#!/usr/bin/env bash
# docker buildx imagetools inspect --raw <image>@<digest>
digest=${5#*@}
f="$FAKE_INDEXES/$digest"
if [[ -f $f ]]; then cat "$f"; else echo '{"mediaType":"application/vnd.oci.image.manifest.v1+json"}'; fi
EOF
chmod +x "$work/bin/gh" "$work/bin/docker"

# NOW is 2026-10-14T12:00:00Z; with KEEP_DAYS=7 the cutoff is 2026-10-07T12:00:00Z.
now=$(date -u -d 2026-10-14T12:00:00Z +%s)
old=2026-10-01T00:00:00Z
new=2026-10-13T00:00:00Z

mkdir -p "$work/indexes"
index() { # index <digest> <child>...
	local d=$1
	shift
	printf '%s\n' "$@" | jq -R . | jq -s '{manifests: map({digest: .})}' >"$work/indexes/$d"
}

v() { # v <id> <digest> <created_at> <tag>...
	local id=$1 d=$2 at=$3
	shift 3
	jq -n --argjson id "$id" --arg d "$d" --arg at "$at" '$ARGS.positional as $t
		| {id: $id, name: $d, created_at: $at, metadata: {container: {tags: $t}}}' --args "$@"
}

{
	v 1 sha256:release "$old" 1.0.0 1.0 1 latest sha-aaaaaaa # a release: kept
	v 2 sha256:rel-amd "$old"                                 # its children: kept
	v 3 sha256:rel-arm "$old"
	v 4 sha256:beta "$old" beta sha-bbbbbbb # :beta, old: kept
	v 5 sha256:beta-amd "$old"
	v 6 sha256:pr-old "$old" sha-ccccccc # an old PR build: deleted
	v 7 sha256:pr-old-amd "$old"         # its children: deleted
	v 8 sha256:pr-old-arm "$old"
	v 9 sha256:pr-new "$new" sha-ddddddd # a recent PR build: kept
	v 10 sha256:pr-new-amd "$new"
	v 11 sha256:orphan-new "$new" # untagged but recent: kept
	v 12 sha256:rc "$old" 1.1.0-rc.1 sha-eeeeeee # a pre-release: kept
} | jq -s . >"$work/versions.json"
index sha256:release sha256:rel-amd sha256:rel-arm
index sha256:beta sha256:beta-amd
index sha256:pr-old sha256:pr-old-amd sha256:pr-old-arm
index sha256:pr-new sha256:pr-new-amd

run() {
	: >"$work/log"
	PATH="$work/bin:$PATH" FAKE_LOG="$work/log" FAKE_VERSIONS="$work/versions.json" \
		FAKE_INDEXES="$work/indexes" NOW=$now OWNER=Mark7888 PACKAGE=two-place-paste-server \
		"$@" "$script"
}

deleted() { grep -- '--method DELETE' "$work/log" | sed 's|.*/versions/||' | sort -n | tr '\n' ' '; }

# --- the rule ---------------------------------------------------------------------------
out=$(run env)
got=$(deleted)
[[ $got == "6 7 8 " ]] || fail "deleted versions: got '$got', want '6 7 8 '"
grep -q 'users/mark7888/packages/container/two-place-paste-server/versions/6' "$work/log" ||
	fail "the owner is not lower-cased in the API path"
[[ $out == *"3 versions deleted"* ]] || fail "summary: $out"

# --- deletion happens in order: the index before its children --------------------------
first=$(grep -- '--method DELETE' "$work/log" | head -1)
[[ $first == */versions/6 ]] || fail "the index is not deleted first: $first"

# --- a dry run deletes nothing ----------------------------------------------------------
out=$(run env DRY_RUN=true)
[[ -z $(deleted) ]] || fail "dry run deleted $(deleted)"
[[ $out == *"would delete sha256:pr-old (sha-ccccccc)"* ]] || fail "dry run output: $out"

# --- a longer retention keeps the old PR build ------------------------------------------
run env KEEP_DAYS=30 >/dev/null
[[ -z $(deleted) ]] || fail "KEEP_DAYS=30 deleted $(deleted)"

# --- an organisation's package uses the orgs/ path --------------------------------------
run env FAKE_OWNER_TYPE=Organization >/dev/null
grep -q 'orgs/mark7888/packages/container' "$work/log" || fail "organisation path not used"

# --- bad input --------------------------------------------------------------------------
if run env KEEP_DAYS=0 >/dev/null 2>&1; then fail "KEEP_DAYS=0 accepted"; fi
if run env PACKAGE= >/dev/null 2>&1; then fail "empty PACKAGE accepted"; fi

if ((failures > 0)); then
	echo "$failures failure(s)" >&2
	exit 1
fi
echo "ok"
