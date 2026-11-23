#!/usr/bin/env bash
# Builds the release archives for every supported platform into dist/.
#
#   scripts/release.sh            # version taken from internal/server/server.go
#   VERSION=1.2.0 scripts/release.sh
#   TARGETS="linux/amd64 windows/amd64" scripts/release.sh
#
# Each archive holds distcache-server, distcache-bench, a sample
# distcache.conf, the LICENSE, NOTICE and README. Linux archives also carry a
# systemd unit. dist/SHA256SUMS lists a checksum per archive.
#
# The binaries are static (CGO_ENABLED=0) and built with -trimpath, so the
# same source and Go toolchain give the same bytes on any build machine.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

SOURCE_VERSION="$(sed -n 's/^var Version = "\(.*\)"$/\1/p' internal/server/server.go | head -n 1)"
VERSION="${VERSION:-$SOURCE_VERSION}"
VERSION="${VERSION#v}"
if [ -z "$VERSION" ]; then
  echo "cannot determine the version; set VERSION=x.y.z" >&2
  exit 1
fi
TARGETS="${TARGETS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64}"
DIST="$ROOT/dist"
LDFLAGS="-s -w -X github.com/rabbicse/distcache/internal/server.Version=$VERSION"

rm -rf "$DIST"
mkdir -p "$DIST"

for target in $TARGETS; do
  os="${target%/*}"
  arch="${target#*/}"
  name="distcache-$VERSION-$os-$arch"
  stage="$DIST/$name"
  ext=""
  if [ "$os" = windows ]; then ext=".exe"; fi
  echo "== $name" >&2
  mkdir -p "$stage"
  for bin in distcache-server distcache-bench; do
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
      go build -trimpath -ldflags "$LDFLAGS" -o "$stage/$bin$ext" "./cmd/$bin"
  done
  cp LICENSE NOTICE README.md deploy/distcache.conf "$stage/"
  if [ "$os" = linux ]; then
    cp deploy/systemd/distcache.service "$stage/"
  fi
  if [ "$os" = windows ]; then
    (cd "$DIST" && zip -q -r "$name.zip" "$name")
  else
    tar -C "$DIST" -czf "$DIST/$name.tar.gz" "$name"
  fi
  rm -rf "$stage"
done

cd "$DIST"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum distcache-* >SHA256SUMS
else
  shasum -a 256 distcache-* >SHA256SUMS
fi
echo "wrote $(ls distcache-* | wc -l | tr -d ' ') archives and SHA256SUMS to $DIST" >&2
