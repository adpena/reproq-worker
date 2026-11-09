#!/bin/sh
# Promote one reviewed Worker draft while preserving the legacy latest route.
set -eu
export LC_ALL=C GH_HOST=github.com GH_PROMPT_DISABLED=1

fail() { printf >&2 'worker publication: %s\n' "$*"; exit 2; }
[ "$#" = 5 ] || fail 'Usage: publish_release.sh OWNER/REPO TAG DIRECTORY MANIFEST_SHA256 CHECKSUMS_SHA256'
repository=$1
tag=$2
directory=$3
printf '%s\n' "$repository" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$' || fail 'Invalid GitHub repository'
if ! command -v gh >/dev/null || ! command -v jq >/dev/null; then
  fail 'gh and jq are required'
fi
scripts_dir=$(CDPATH='' cd "$(dirname "$0")" && pwd)
"$scripts_dir/verify_release_bundle.sh" "$directory" "$4" "$5" >/dev/null

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d ' ' -f 1
  else
    shasum -a 256 "$1" | cut -d ' ' -f 1
  fi
}

record_asset() {
  printf '%s\tsha256:%s\t%s\n' "$1" "$2" "$(wc -c < "$directory/$1" | tr -d ' ')"
}

# Freeze the reviewed metadata used for promotion. Never derive expected remote
# digests from reopening mutable artifact paths after bundle verification.
manifest=$(cat "$directory/BUILD-MANIFEST.txt")
checksums=$(cat "$directory/SHA256SUMS")
[ "$(printf '%s\n' "$manifest" | hash_file -)" = "$4" ] || fail 'Manifest changed during verification'
[ "$(printf '%s\n' "$checksums" | hash_file -)" = "$5" ] || fail 'Checksums changed during verification'
version=$(printf '%s\n' "$manifest" | sed -n 's/^version=//p')
revision=$(printf '%s\n' "$manifest" | sed -n 's/^source_revision=//p')
tree=$(printf '%s\n' "$manifest" | sed -n 's/^source_tree=//p')
[ "$(printf '%s\n' "$manifest" | sed -n 's/^release_status=//p')" = TAGGED_DRAFT_CANDIDATE ] &&
  [ "$tag" = "v$version" ] || fail 'Bundle and requested tag differ'

names=' SHA256SUMS '
records=$(record_asset SHA256SUMS "$5")
while read -r digest name; do
  if [ "$name" = BUILD-MANIFEST.txt ] && [ "$digest" != "$4" ]; then
    fail 'Checksum set names a different manifest'
  fi
  sidecar="$directory/$name.sha256"
  [ -f "$sidecar" ] && [ ! -L "$sidecar" ] || fail "Missing regular sidecar: $name"
  checksum_line=$(printf '%s  %s' "$digest" "$name")
  printf '%s\n' "$checksum_line" | cmp -s "$sidecar" - || fail "Sidecar differs: $name"
  sidecar_digest=$(printf '%s\n' "$checksum_line" | hash_file -)
  names="$names$name $name.sha256 "
  records="$records
$(record_asset "$name" "$digest")
$(record_asset "$name.sha256" "$sidecar_digest")"
done <<EOF
$checksums
EOF
for entry in "$directory"/* "$directory"/.[!.]* "$directory"/..?*; do
  [ -e "$entry" ] || [ -L "$entry" ] || continue
  case "$names" in *" ${entry##*/} "*) ;; *) fail "Unexpected bundle entry: ${entry##*/}" ;; esac
done
local_assets=$(printf '%s\n' "$records" |
  jq -Rn '[inputs | split("\t") | {name:.[0],digest:.[1],size:(.[2]|tonumber)}] | sort_by(.name)')

api() {
  endpoint=$1
  shift
  gh api --hostname github.com "repos/$repository/$endpoint" "$@"
}

check_tag() {
  api "commits/refs/tags/$tag" |
    jq -e --arg revision "$revision" --arg tree "$tree" '.sha == $revision and .commit.tree.sha == $tree' >/dev/null ||
    fail 'Remote tag does not identify the reviewed commit and tree'
}

release_view() { gh release view "$tag" --repo "$repository" --json databaseId,tagName,isDraft,isPrerelease,assets; }
asset_inventory() {
  jq -ce '.assets | select(all(.[]; .state == "uploaded" and (.id | type) == "string")) |
    map({id,name,digest,size}) | sort_by(.name)'
}

check_tag
latest=$(api releases/latest --jq .tag_name)
[ -n "$latest" ] || fail 'The current latest release is unknown'
if [ "$(printf '%s' "$repository" | tr '[:upper:]' '[:lower:]')" = adpena/reproq-worker ] && [ "$latest" != v0.0.144 ]; then
  fail 'Restore the legacy v0.0.144 latest pointer before promotion'
fi
draft=$(release_view)
release_id=$(printf '%s\n' "$draft" | jq -er --arg tag "$tag" '
  select(.tagName == $tag and .isDraft == true and .isPrerelease == false) | .databaseId | numbers') ||
  fail 'Expected one unpublished stable draft for the reviewed tag'
remote_assets=$(printf '%s\n' "$draft" | asset_inventory) || fail 'The draft has assets that are not uploaded'
printf '%s\n' "$remote_assets" | jq -e --argjson expected "$local_assets" 'map(del(.id)) == $expected' >/dev/null ||
  fail 'Draft assets differ from the verified bundle'

# The draft's numeric ID is the mutation target. Reconcile ambiguous responses through readback.
api "releases/$release_id" --method PATCH -F draft=false -f make_latest=false >/dev/null ||
  fail 'Publication response is uncertain. Inspect the release and latest pointer before taking further action.'

published=$(release_view) || fail 'Publication readback failed. Inspect the release and latest pointer before taking further action.'
printf '%s\n' "$published" | jq -e --arg tag "$tag" --argjson id "$release_id" '
  .databaseId == $id and .tagName == $tag and .isDraft == false and .isPrerelease == false' >/dev/null ||
  fail 'The release readback differs. Inspect the release before taking further action.'
[ "$(printf '%s\n' "$published" | asset_inventory)" = "$remote_assets" ] || fail 'Published assets differ from the verified draft'
[ "$(api releases/latest --jq .tag_name)" = "$latest" ] || fail 'The latest pointer changed during publication'
check_tag
printf 'Published %s at %s. Verified %s remains latest.\n' "$tag" "$revision" "$latest"
