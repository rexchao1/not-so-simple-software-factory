#!/bin/sh

set -eu

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
  echo "usage: $0 VERSION COMMIT [OUTPUT]" >&2
  exit 2
fi

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
version=$1
commit=$2
output=${3:-dist}
toolchain=$(tr -d '[:space:]' < "$root/.release/go-version")
release_cache=$(mktemp -d)
cleanup() {
  chmod -R u+w "$release_cache" 2>/dev/null || true
  rm -rf "$release_cache"
}
trap cleanup EXIT
module_cache=${FACTORY_RELEASE_GOMODCACHE:-$release_cache/modules}
build_cache=${FACTORY_RELEASE_GOCACHE:-$release_cache/build}
mkdir -p "$module_cache" "$build_cache"

if [ -z "$toolchain" ]; then
  echo "release Go version is empty" >&2
  exit 1
fi

cd "$root"
GO111MODULE=on GOARCH= GOAMD64=v1 GOARM64=v8.0 GODEBUG= GOENV=off \
  GOEXPERIMENT= GOFIPS140=off GOFLAGS= GOOS= GOWORK=off \
  GOMODCACHE="$module_cache" GOCACHE="$build_cache" \
  FACTORY_RELEASE_GOMODCACHE="$module_cache" \
  FACTORY_RELEASE_GOCACHE="$build_cache" \
  GOTOOLCHAIN="go$toolchain" \
  go run ./cmd/release-artifacts \
  -root "$root" -output "$output" -version "$version" -commit "$commit"
