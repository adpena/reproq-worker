#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
# Source identity always belongs to this checkout, not an inherited Git index
# or worktree selected by a caller's environment.
unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_NAMESPACE

fail() { echo "release build: $*" >&2; exit 2; }
usage() { echo "Usage: $0 (--unreleased | --tag EXISTING_TAG) --output NEW_DIRECTORY" >&2; exit 2; }

source "$root/scripts/release_source.sh"

mode=""
tag=""
output=""
while (($#)); do
  case "$1" in
    --unreleased) [[ -z "$mode" ]] || usage; mode=UNRELEASED; shift ;;
    --tag) [[ -z "$mode" && $# -ge 2 ]] || usage; mode=TAGGED_DRAFT_CANDIDATE; tag="$2"; shift 2 ;;
    --output) [[ -z "$output" && $# -ge 2 ]] || usage; output="$2"; shift 2 ;;
    *) usage ;;
  esac
done
[[ -n "$mode" && -n "$output" ]] || usage
[[ ! -e "$output" && ! -L "$output" ]] || fail "output directory already exists: $output"
check_source

release_source_identity
if [[ "$mode" == TAGGED_DRAFT_CANDIDATE ]]; then
  [[ "$tag" == "v$version" ]] || fail "tag must match Version: v$version"
  git show-ref --verify --quiet "refs/tags/$tag" || fail "tag must already exist locally: $tag"
  [[ "$(git rev-parse --verify "refs/tags/$tag^{commit}")" == "$revision" ]] || fail "tag does not identify HEAD"
fi

# Ignore user-level Go configuration, workspace selection, CPU tuning, and
# flags. Externally configured module/build caches do not change build policy.
export GO111MODULE=on GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS= GOEXPERIMENT= CGO_ENABLED=0 GOAMD64=v1 GOARM64=v8.0 LC_ALL=C
go_version="$(go version)"
source "$root/scripts/check_tools.sh"
require_go_toolchain
hash_binary() {
  local value ignored
  if command -v sha256sum >/dev/null 2>&1; then
    read -r value ignored < <(sha256sum "$1")
  elif command -v shasum >/dev/null 2>&1; then
    read -r value ignored < <(shasum -a 256 "$1")
  else
    fail "sha256sum or shasum is required"
  fi
  [[ "$value" =~ ^[0-9a-f]{64}$ ]] || fail "cannot hash the selected Go executable"
  printf '%s' "$value"
}
go_binary_sha256="$(hash_binary "$go_binary_path")"
mkdir -p "$(dirname "$output")"
staging="$(mktemp -d "${output}.tmp.XXXXXX")"
output_claimed=0
cleanup() {
  result=$?
  rm -rf "$staging"
  if [[ "$result" != 0 && "$output_claimed" == 1 ]]; then
    # Never recursively remove a publication path: on I/O failure leave any
    # incomplete files for inspection, and preserve unexpected foreign files.
    rmdir "$output" 2>/dev/null || echo "release build: incomplete owned output retained at $output" >&2
  fi
}
trap cleanup EXIT

platforms=(darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64)
artifacts=()
for platform in "${platforms[@]}"; do
  target_os="${platform%/*}"
  target_arch="${platform#*/}"
  name="reproq-$target_os-$target_arch"
  [[ "$target_os" != windows ]] || name="$name.exe"
  echo "Building $platform ($mode)..."
  GOOS="$target_os" GOARCH="$target_arch" go build -mod=readonly -trimpath -buildvcs=true -tags prod -o "$staging/$name" ./cmd/reproq
  artifacts+=("$name")
done

cat > "$staging/BUILD-MANIFEST.txt" <<EOF
release_status=$mode
version=$version
tag=$tag
source_revision=$revision
source_tree=$tree
compiler=$go_version
go_binary_sha256=$go_binary_sha256
build_flags=-mod=readonly -trimpath -buildvcs=true -tags prod
CGO_ENABLED=0
GOAMD64=v1
GOARM64=v8.0
GOEXPERIMENT=
platforms=${platforms[*]}
EOF
artifacts+=(BUILD-MANIFEST.txt)
(
  cd "$staging"
  for name in "${artifacts[@]}"; do
    if command -v sha256sum >/dev/null 2>&1; then
      sha256sum "$name" > "$name.sha256"
    elif command -v shasum >/dev/null 2>&1; then
      shasum -a 256 "$name" > "$name.sha256"
    else
      fail "sha256sum or shasum is required"
    fi
    cat "$name.sha256"
  done > SHA256SUMS
)

require_source_identity
require_go_toolchain
[[ "$(hash_binary "$go_binary_path")" == "$go_binary_sha256" ]] || fail "Go executable changed during build"
# mkdir is the exclusive publication claim. A competing directory is an error,
# never a destination into which mv may silently nest our staging directory.
mkdir "$output" || fail "output appeared during build; it was not changed"
output_claimed=1
publication=()
for name in "${artifacts[@]}"; do
  [[ "$name" == BUILD-MANIFEST.txt ]] || publication+=("$name")
  publication+=("$name.sha256")
done
publication+=(SHA256SUMS BUILD-MANIFEST.txt)
for name in "${publication[@]}"; do
  cp -p -n "$staging/$name" "$output/$name"
  [[ -f "$output/$name" && ! -L "$output/$name" ]] && cmp -s "$staging/$name" "$output/$name" || fail "published asset does not match: $name"
done
rm -rf "$staging"
trap - EXIT
echo "Review $mode artifacts and checksums in $output; no tag or release was created."
