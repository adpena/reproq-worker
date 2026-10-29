#!/usr/bin/env bash
# go.mod is the compiler authority; Makefile supplies the linter source version.
set -euo pipefail

require_go_toolchain() {
  local tool_root
  tool_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  required_go_patch="$(sed -n 's/^go \([0-9][0-9.]*\)$/\1/p' "$tool_root/go.mod")"
  [[ "$required_go_patch" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Expected one exact Go patch in go.mod.' >&2; return 2; }
  actual_go_patch="$(GOENV=off GOWORK=off GOTOOLCHAIN=local go env GOVERSION)"
  [[ "$actual_go_patch" == "go$required_go_patch" ]] || {
    printf >&2 'Canonical checks/builds require Go %s from go.mod; found %s.\n' "$required_go_patch" "$actual_go_patch"
    return 2
  }
  go_binary_path="$(command -v go)"
  [[ -f "$go_binary_path" ]] || { echo 'Go must resolve to a regular executable file.' >&2; return 2; }
}

require_linter() {
  local required_linter="$1" version
  [[ "$required_linter" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 2
  version="$(golangci-lint version)"
  case "$version" in
    "golangci-lint has version ${required_linter#v} built with go$required_go_patch "*) ;;
    *) printf >&2 'Canonical lint requires %s built with Go %s; found %s\n' "$required_linter" "$required_go_patch" "$version"; return 2 ;;
  esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  require_go_toolchain
  if [[ "${1:-}" == --linter && $# == 2 ]]; then
    require_linter "$2"
  elif (($#)); then
    echo 'Usage: scripts/check_tools.sh [--linter VERSION]' >&2
    exit 2
  fi
fi
