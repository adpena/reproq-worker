#!/usr/bin/env bash
# Package canonical output; this script never compiles a worker or publishes it.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_NAMESPACE
export GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS= GOEXPERIMENT=
fail() { printf >&2 'container build: %s\n' "$*"; exit 2; }
source "$root/scripts/release_source.sh"
source "$root/scripts/check_tools.sh"
artifacts= image= platform=
while (($#)); do
  case "$1" in
    --artifacts) [[ $# -ge 2 && -z "$artifacts" ]] || fail 'invalid --artifacts'; artifacts=$2; shift 2;;
    --image) [[ $# -ge 2 && -z "$image" ]] || fail 'invalid --image'; image=$2; shift 2;;
    --platform) [[ $# -ge 2 && -z "$platform" ]] || fail 'invalid --platform'; platform=$2; shift 2;;
    *) fail 'Usage: build_container.sh --artifacts DIRECTORY --image LOCAL_IMAGE [--platform linux/ARCH]';;
  esac
done
[[ -n "$artifacts" && -n "$image" && "$image" != -* ]] || fail 'artifact directory and local image name are required'
[[ -z "$platform" || "$platform" =~ ^linux/[a-z0-9]+$ ]] || fail 'expected one Linux target platform'
check_source
release_source_identity
require_go_toolchain
[[ -d "$artifacts" && ! -L "$artifacts" ]] || fail 'artifact directory must exist and cannot be a symlink'
artifacts="$(cd "$artifacts" && pwd -P)"
hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d ' ' -f 1
  else shasum -a 256 "$1" | cut -d ' ' -f 1; fi
}
manifest_sha="$(hash_file "$artifacts/BUILD-MANIFEST.txt")"
checksums_sha="$(hash_file "$artifacts/SHA256SUMS")"
metadata="$(sh scripts/verify_release_bundle.sh "$artifacts" "$manifest_sha" "$checksums_sha")"
IFS=$'\t' read -r bundle_version bundle_revision bundle_tree bundle_mode bundle_compiler bundle_platforms <<< "$metadata"
[[ "$bundle_version" == "$version" && "$bundle_revision" == "$revision" && "$bundle_tree" == "$tree" ]] || fail 'bundle does not identify this exact committed source'
[[ "$bundle_compiler" == "$actual_go_patch" ]] || fail 'bundle compiler differs from the canonical source toolchain'
for target in $bundle_platforms; do
  name="$(sh scripts/verify_release_bundle.sh "$artifacts" "$manifest_sha" "$checksums_sha" "${target%/*}" "${target#*/}")"
  # go version -m reads build metadata; it does not execute the candidate.
  information="$(go version -m "$artifacts/$name")"
  first_line=${information%%$'\n'*}
  [[ "$first_line" == *": $bundle_compiler" ]] || fail "wrong compiler in $name"
  for expected in "vcs.revision=$revision" 'vcs.modified=false' "GOOS=${target%/*}" "GOARCH=${target#*/}" '-tags=prod' '-trimpath=true' 'CGO_ENABLED=0'; do
    printf '%s\n' "$information" | grep -Fqx -- $'\tbuild\t'"$expected" || fail "build metadata differs in $name: $expected"
  done
done
context="$(mktemp -d "${TMPDIR:-/tmp}/reproq-container.XXXXXX")"
trap 'rm -rf "$context"' EXIT
mkdir "$context/artifacts" "$context/scripts"
for target in $bundle_platforms; do
  name="$(sh scripts/verify_release_bundle.sh "$artifacts" "$manifest_sha" "$checksums_sha" "${target%/*}" "${target#*/}")"
  cp -p "$artifacts/$name" "$context/artifacts/$name"
done
cp -p "$artifacts/BUILD-MANIFEST.txt" "$artifacts/SHA256SUMS" "$context/artifacts/"
while IFS= read -r -d '' name; do
  mkdir -p "$(dirname "$context/$name")"
  cp -p "$name" "$context/$name"
  expected="$(git rev-parse "$revision:$name")"
  [[ "$(git hash-object --no-filters -- "$context/$name")" == "$expected" ]] || fail "source copy differs: $name"
done < <(git ls-files -z -- Dockerfile scripts/verify_release_bundle.sh)
sh scripts/verify_release_bundle.sh "$context/artifacts" "$manifest_sha" "$checksums_sha" >/dev/null
require_source_identity
arguments=(buildx build --load --tag "$image" --build-arg "EXPECTED_MANIFEST_SHA256=$manifest_sha"
  --build-arg "EXPECTED_CHECKSUMS_SHA256=$checksums_sha" --build-arg "SOURCE_REVISION=$revision"
  --build-arg "WORKER_VERSION=$version" --build-arg "RELEASE_STATUS=$bundle_mode")
[[ -z "$platform" ]] || arguments+=(--platform "$platform")
docker "${arguments[@]}" "$context"
require_source_identity
printf 'Built local container %s from %s %s; no image or worker release was published.\n' "$image" "$bundle_mode" "$revision"
