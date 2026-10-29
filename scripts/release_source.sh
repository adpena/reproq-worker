#!/usr/bin/env bash
# Shared source checks for the canonical builder and its artifact consumers.
# Callers provide fail(), select this checkout, and enable pipefail.

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


release_source_identity() {
  declarations="$(grep -c '^const Version[[:space:]]*=' cmd/reproq/main.go || true)"
  version="$(sed -n 's/^const Version = "\([0-9][0-9.]*\)"$/\1/p' cmd/reproq/main.go)"
  [[ "$declarations" == 1 && "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "expected one numeric Version constant"
  revision="$(git rev-parse --verify HEAD)"
  tree="$(git rev-parse --verify 'HEAD^{tree}')"
}

require_source_identity() {
  [[ "$revision" == "$(git rev-parse --verify HEAD)" && "$tree" == "$(git rev-parse --verify 'HEAD^{tree}')" ]] || fail 'source revision changed during build'
  check_source
}

# Inspect cross-platform binaries without executing them. Both the canonical
# builder and container consumer require the same embedded source/toolchain.
require_binary_build_metadata() {
  local binary="$1" target="$2" compiler="$3" information first_line expected
  information="$(go version -m "$binary")" || fail "cannot inspect build metadata: $binary"
  first_line=${information%%$'\n'*}
  [[ "$first_line" == *": $compiler" ]] || fail "wrong compiler in $binary"
  for expected in "vcs.revision=$revision" 'vcs.modified=false' "GOOS=${target%/*}" "GOARCH=${target#*/}" '-tags=prod' '-trimpath=true' 'CGO_ENABLED=0'; do
    printf '%s\n' "$information" | grep -Fqx -- $'\tbuild\t'"$expected" || fail "build metadata differs in $binary: $expected; verify checkout and Go VCS discovery"
  done
}
