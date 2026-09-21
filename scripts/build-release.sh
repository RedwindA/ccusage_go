#!/bin/sh
set -eu

version=${VERSION:?Set VERSION to the release tag (for example v0.15.0)}
os=${GOOS:?Set GOOS to linux, darwin, or windows}
arch=${GOARCH:?Set GOARCH to amd64 or arm64}
case "$version" in *[!a-zA-Z0-9._-]*|'') echo 'Invalid VERSION' >&2; exit 1 ;; esac
case "$os/$arch" in
  linux/amd64|linux/arm64|darwin/amd64|darwin/arm64|windows/amd64|windows/arm64) ;;
  *) echo "Unsupported release target: $os/$arch" >&2; exit 1 ;;
esac

mkdir -p dist
binary=ccusage_go-$os-$arch
if [ "$os" = windows ]; then binary=$binary.exe; fi
CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
  -ldflags="-s -w -X main.version=$version" -o "dist/$binary" ./cmd/ccusage
if [ "$os" = windows ]; then
  (cd dist && zip -q -j "$binary.zip" "$binary" ../LICENSE)
else
  tar -czf "dist/$binary.tar.gz" -C dist "$binary" -C .. LICENSE
fi
