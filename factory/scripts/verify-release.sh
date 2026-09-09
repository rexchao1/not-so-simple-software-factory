#!/bin/sh

set -eu

if [ "$#" -lt 4 ] || [ "$#" -gt 5 ]; then
  echo "usage: $0 DIRECTORY VERSION COMMIT OS/ARCH [execute]" >&2
  exit 2
fi

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
directory=$1
version=$2
commit=$3
target=$4
mode=${5:-}
toolchain=$(tr -d '[:space:]' < "$root/.release/go-version")
execute=
if [ "$mode" = "execute" ]; then
  execute=-execute
elif [ -n "$mode" ]; then
  echo "verification mode must be execute when provided" >&2
  exit 2
fi

cd "$root"
GO111MODULE=on GOARCH= GOAMD64=v1 GOARM64=v8.0 GODEBUG= GOENV=off \
  GOEXPERIMENT= GOFIPS140=off GOFLAGS= GOOS= GOWORK=off \
  GOTOOLCHAIN="go$toolchain" \
  exec go run ./cmd/release-artifacts \
  -root "$root" -output "$directory" -version "$version" -commit "$commit" \
  -verify-target "$target" $execute
