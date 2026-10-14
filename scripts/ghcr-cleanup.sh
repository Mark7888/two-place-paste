#!/usr/bin/env bash
# Delete the server image's short-lived versions from the GitHub Container
# Registry (docker-cleanup.yml).
#
#   scripts/ghcr-cleanup.sh
#
# The registry deletes a whole image (a digest), never one tag of it, so the
# rule is about images, not tags:
#
#   - an image whose tags are all sha-* and that is older than KEEP_DAYS is
#     deleted. The image :beta or a version tag points at also carries a sha-*
#     tag; it is kept, because not all of its tags are sha-*.
#   - an untagged image older than KEEP_DAYS is deleted, unless an image that is
#     kept lists it. A multi-platform image is an index whose per-platform
#     images are stored as untagged versions of their own, and deleting those
#     would break the index. An image is untagged otherwise only when a tag
#     moved off it, as on a re-run of the same release.
#
# Every decision is made before anything is deleted, so a failure part way
# through reading leaves the registry as it was.
#
# Inputs, from the environment:
#   GH_TOKEN        for the gh CLI; needs packages: write on the package
#   OWNER           the user or organisation that owns the package
#   PACKAGE         the package (image) name, e.g. two-place-paste-server
#   KEEP_DAYS       how long sha-only and untagged images are kept (default 7)
#   DRY_RUN         "true" lists what would be deleted and deletes nothing
#   NOW             the current time as Unix seconds; for the tests
#
# Reading an image's index needs `docker buildx imagetools` and, for a private
# package, a `docker login ghcr.io` beforehand.

set -euo pipefail

die() {
	echo "ghcr-cleanup.sh: $*" >&2
	exit 1
}

for var in OWNER PACKAGE; do
	[[ -n ${!var:-} ]] || die "$var is not set"
done
keep_days=${KEEP_DAYS:-7}
[[ $keep_days =~ ^[1-9][0-9]*$ ]] || die "KEEP_DAYS '$keep_days' is not a positive integer"
now=${NOW:-$(date -u +%s)}
account=${OWNER,,}
image="ghcr.io/$account/$PACKAGE"

# GitHub's created_at is an ISO-8601 UTC string, so a string comparison with
# one in the same format orders them.
cutoff_s=$((now - keep_days * 86400))
cutoff=$(date -u -d "@$cutoff_s" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null ||
	date -u -r "$cutoff_s" +%Y-%m-%dT%H:%M:%SZ)

# The REST paths differ for a user's and an organisation's packages.
if [[ $(gh api "users/$account" --jq .type) == Organization ]]; then
	base="orgs/$account/packages/container/$PACKAGE"
else
	base="users/$account/packages/container/$PACKAGE"
fi

versions=$(gh api --paginate "$base/versions?per_page=100" | jq -s 'add // []')
echo "$(jq length <<<"$versions") versions of $image; deleting sha-only and untagged ones created before $cutoff"

# id<TAB>digest<TAB>tags, for the images whose tags are all sha-*.
sha_only=$(jq -r --arg cutoff "$cutoff" '.[]
	| (.metadata.container.tags // []) as $tags
	| select(($tags | length) > 0 and all($tags[]; startswith("sha-")) and .created_at < $cutoff)
	| [.id, .name, ($tags | join(","))] | @tsv' <<<"$versions")

# Every image that stays and has a tag: the indexes whose children are kept.
kept=$(jq -r --arg cutoff "$cutoff" '.[]
	| (.metadata.container.tags // []) as $tags
	| select(($tags | length) > 0)
	| select((all($tags[]; startswith("sha-")) and .created_at < $cutoff) | not)
	| .name' <<<"$versions")

children=""
while IFS= read -r digest; do
	[[ -n $digest ]] || continue
	children+=$(docker buildx imagetools inspect --raw "$image@$digest" |
		jq -r '.manifests[]?.digest')$'\n'
done <<<"$kept"

untagged=$(jq -r --arg cutoff "$cutoff" --arg children "$children" '
	($children | split("\n") | map(select(. != ""))) as $keep
	| .[]
	| select(((.metadata.container.tags // []) | length) == 0 and .created_at < $cutoff)
	| select(.name as $n | any($keep[]; . == $n) | not)
	| [.id, .name, "untagged"] | @tsv' <<<"$versions")

# The indexes first, then the untagged images, which include their children.
plan=$(printf '%s\n%s\n' "$sha_only" "$untagged" | sed '/^$/d')
if [[ -z $plan ]]; then
	echo "nothing to delete"
	exit 0
fi

count=0
while IFS=$'\t' read -r id digest tags; do
	if [[ ${DRY_RUN:-false} == true ]]; then
		echo "would delete $digest ($tags)"
	else
		echo "deleting $digest ($tags)"
		gh api --method DELETE "$base/versions/$id" >/dev/null
	fi
	count=$((count + 1))
done <<<"$plan"

if [[ ${DRY_RUN:-false} == true ]]; then
	echo "$count versions would be deleted (dry run)"
else
	echo "$count versions deleted"
fi
