#!/bin/sh
# Consumer verification only. build_releases.sh owns compilation and platforms.
set -eu
set -f
export LC_ALL=C
fail() { printf >&2 'release bundle: %s\n' "$*"; exit 2; }
hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d ' ' -f 1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d ' ' -f 1
  else
    fail 'sha256sum or shasum is required'
  fi
}
valid_hash() { [ "${#1}" = 64 ] && case "$1" in *[!0-9a-f]*) return 1;; esac; }
valid_revision() { [ "${#1}" = 40 ] && case "$1" in *[!0-9a-f]*) return 1;; esac; }
[ "$#" = 3 ] || [ "$#" = 5 ] || fail 'Usage: verify_release_bundle.sh DIRECTORY MANIFEST_SHA256 CHECKSUMS_SHA256 [OS ARCH]'
directory=$1
expected_manifest=$2
expected_checksums=$3
target_os=${4:-}
target_arch=${5:-}
selection=0
if [ "$#" = 5 ]; then
  [ -n "$target_os" ] && [ -n "$target_arch" ] || fail 'OS and architecture are required together'
  selection=1
fi
valid_hash "$expected_manifest" && valid_hash "$expected_checksums" || fail 'expected SHA256 values are required'
[ -d "$directory" ] && [ ! -L "$directory" ] || fail 'bundle must be a real directory'
cd "$directory"
for name in BUILD-MANIFEST.txt SHA256SUMS; do
  [ -f "$name" ] && [ ! -L "$name" ] || fail "missing regular $name"
done
[ "$(hash_file BUILD-MANIFEST.txt)" = "$expected_manifest" ] || fail 'manifest digest differs'
[ "$(hash_file SHA256SUMS)" = "$expected_checksums" ] || fail 'checksum-set digest differs'
seen='|'
version= revision= tree= status= tag= platforms= compiler=
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in *=*) ;; *) fail 'malformed manifest line';; esac
  key=${line%%=*}
  value=${line#*=}
  case "$key" in ''|*[!A-Za-z0-9_]*) fail 'invalid manifest key';; esac
  case "$seen" in *"|$key|"*) fail "duplicate manifest key: $key";; esac
  seen=$seen$key'|'
  case "$key" in
    version) version=$value;;
    source_revision) revision=$value;;
    source_tree) tree=$value;;
    release_status) status=$value;;
    tag) tag=$value;;
    platforms) platforms=$value;;
    compiler) compiler=$value;;
  esac
done < BUILD-MANIFEST.txt
printf '%s\n' "$version" | awk -F. 'NF!=3 {exit 1} {for(i=1;i<=3;i++) if($i !~ /^[0-9]+$/) exit 1}' || fail 'invalid version'
valid_revision "$revision" && valid_revision "$tree" || fail 'invalid source identity'
case "$status:$tag" in
  UNRELEASED:) ;;
  TAGGED_DRAFT_CANDIDATE:v"$version") ;;
  *) fail 'invalid release mode/tag association';;
esac
set -- $compiler
[ "$#" = 4 ] && [ "$1" = go ] && [ "$2" = version ] || fail 'invalid compiler identity'
compiler_version=$3
printf '%s\n' "$compiler_version" | awk '/^go[0-9]+\.[0-9]+\.[0-9]+$/ {ok=1} END {exit !ok}' || fail 'invalid compiler version'
[ -n "$platforms" ] || fail 'missing platforms'
expected_names=' BUILD-MANIFEST.txt '
for platform in $platforms; do
  case "$platform" in *[!a-z0-9/]*|/*|*/|*/*/*) fail 'invalid platform';; */*) ;; *) fail 'invalid platform';; esac
  os=${platform%/*}
  arch=${platform#*/}
  name=reproq-$os-$arch
  [ "$os" != windows ] || name=$name.exe
  case "$expected_names" in *" $name "*) fail 'duplicate platform';; esac
  expected_names=$expected_names$name' '
done
verified_names=' '
while read -r expected name extra || [ -n "$expected$name$extra" ]; do
  [ -n "$expected" ] && [ -n "$name" ] && [ -z "$extra" ] || fail 'malformed checksum line'
  valid_hash "$expected" || fail 'invalid checksum'
  case "$name" in ''|*[!A-Za-z0-9._-]*|.*) fail 'unsafe checksum path';; esac
  case "$expected_names" in *" $name "*) ;; *) fail 'checksum names an unexpected artifact';; esac
  case "$verified_names" in *" $name "*) fail 'duplicate checksum';; esac
  [ -f "$name" ] && [ ! -L "$name" ] || fail "missing regular artifact: $name"
  [ "$(hash_file "$name")" = "$expected" ] || fail "artifact digest differs: $name"
  verified_names=$verified_names$name' '
done < SHA256SUMS
for name in $expected_names; do
  case "$verified_names" in *" $name "*) ;; *) fail "missing checksum: $name";; esac
done
if [ "$selection" = 1 ]; then
  target=$target_os/$target_arch
  case " $platforms " in *" $target "*) ;; *) fail 'target is absent from canonical platforms';; esac
  name=reproq-${target%/*}-${target#*/}
  [ "${target%/*}" != windows ] || name=$name.exe
  printf '%s\n' "$name"
else
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$version" "$revision" "$tree" "$status" "$compiler_version" "$platforms"
fi
