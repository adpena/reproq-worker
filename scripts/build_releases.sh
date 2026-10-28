#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
# Source identity always belongs to this checkout, not an inherited Git index
# or worktree selected by a caller's environment.
unset GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_NAMESPACE

fail() { echo "release build: $*" >&2; exit 2; }
usage() { echo "Usage: $0 (--unreleased | --tag EXISTING_TAG) --output NEW_DIRECTORY" >&2; exit 2; }

check_source() {
  local entry metadata path file_mode expected stage actual changes
  # Git status deliberately trusts these flags. Release builds cannot: they
  # must describe committed source even when a caller normally hides changes.
  if ! git ls-files -v -z | while IFS= read -r -d '' entry; do
    case "${entry:0:1}" in [a-zS]) exit 1 ;; esac
  done; then
    fail "assume-unchanged and skip-worktree index flags are forbidden"
  fi
  changes="$(git status --porcelain --untracked-files=normal)" || fail "cannot inspect source checkout"
  [[ -z "$changes" ]] || fail "source checkout must be clean"
  # Check real file bytes too, rather than depending on cached stat metadata.
  # This repository's release source consists of regular tracked files.
  if ! git ls-files --stage -z | while IFS= read -r -d '' entry; do
    metadata="${entry%%$'\t'*}"
    path="${entry#*$'\t'}"
    read -r file_mode expected stage <<< "$metadata"
    [[ "$stage" == 0 && ( "$file_mode" == 100644 || "$file_mode" == 100755 ) && -f "$path" && ! -L "$path" ]] || exit 1
    actual="$(git hash-object --no-filters -- "$path")" || exit 1
    [[ "$actual" == "$expected" ]] || exit 1
  done; then
    fail "tracked release source bytes must match the committed regular files"
  fi
}

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

declarations="$(grep -c '^const Version[[:space:]]*=' cmd/reproq/main.go || true)"
version="$(sed -n 's/^const Version = "\([0-9][0-9.]*\)"$/\1/p' cmd/reproq/main.go)"
[[ "$declarations" == 1 && "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "expected one numeric Version constant"
revision="$(git rev-parse --verify HEAD)"
tree="$(git rev-parse --verify 'HEAD^{tree}')"
if [[ "$mode" == TAGGED_DRAFT_CANDIDATE ]]; then
  [[ "$tag" == "v$version" ]] || fail "tag must match Version: v$version"
  git show-ref --verify --quiet "refs/tags/$tag" || fail "tag must already exist locally: $tag"
  [[ "$(git rev-parse --verify "refs/tags/$tag^{commit}")" == "$revision" ]] || fail "tag does not identify HEAD"
fi

# Ignore user-level Go configuration, workspace selection, CPU tuning, and
# flags. Externally configured module/build caches do not change build policy.
export GO111MODULE=on GOENV=off GOWORK=off GOTOOLCHAIN=local GOFLAGS= GOEXPERIMENT= CGO_ENABLED=0 GOAMD64=v1 GOARM64=v8.0 LC_ALL=C
go_version="$(go version)"
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

artifacts=()
for platform in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64; do
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
build_flags=-mod=readonly -trimpath -buildvcs=true -tags prod
CGO_ENABLED=0
GOAMD64=v1
GOARM64=v8.0
GOEXPERIMENT=
platforms=darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64
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

[[ "$revision" == "$(git rev-parse --verify HEAD)" ]] || fail "source revision changed during build"
check_source
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
