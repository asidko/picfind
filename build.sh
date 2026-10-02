#!/bin/sh
set -eu
cd "$(dirname "$0")"

VERSION="${VERSION:-dev}"
OUT=dist

build() {
	name_os=$1
	name_arch=$2
	[ "$1" = darwin ] && name_os=macos
	[ "$2" = amd64 ] && name_arch=x86_64
	name="picfind-$name_os-$name_arch"
	CGO_ENABLED=0 GOOS="$1" GOARCH="$2" GOAMD64=v2 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$OUT/$name" .
	echo "built $OUT/$name"
}

checksums() {
	cd "$OUT"
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum picfind-* > SHA256SUMS
	else
		shasum -a 256 picfind-* > SHA256SUMS
	fi
	cat SHA256SUMS
}

mkdir -p "$OUT"
case "${1:-}" in
	all)
		rm -f "$OUT"/picfind-* "$OUT"/SHA256SUMS
		build linux amd64
		build linux arm64
		build darwin arm64
		build darwin amd64
		checksums
		;;
	"")
		build "$(go env GOOS)" "$(go env GOARCH)"
		;;
	*)
		echo "usage: ./build.sh [all]" >&2
		exit 2
		;;
esac
