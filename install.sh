#!/bin/sh
set -eu

REPO="asidko/picfind"
BIN="picfind"
INSTALL_DIR="${PICFIND_INSTALL_DIR:-$HOME/.local/bin}"
TAG=""
OS=$(uname -s)

detect_target() {
    case "$OS" in
        Linux) os=linux ;;
        Darwin) os=macos ;;
        *) echo "unsupported OS: $OS - build from source: https://github.com/${REPO}#build-from-source" >&2; exit 1 ;;
    esac
    case "$(uname -m)" in
        x86_64|amd64) arch=x86_64 ;;
        aarch64|arm64) arch=arm64 ;;
        *) echo "unsupported CPU: $(uname -m) - build from source: https://github.com/${REPO}#build-from-source" >&2; exit 1 ;;
    esac
    echo "${os}-${arch}"
}

do_remove() {
    if [ -f "$INSTALL_DIR/$BIN" ]; then
        rm -f "$INSTALL_DIR/$BIN"
        echo "removed $INSTALL_DIR/$BIN"
    else
        echo "$BIN not installed in $INSTALL_DIR"
    fi
    echo "$BIN keeps no config or data files, nothing else to clean up"
    exit 0
}

while [ $# -gt 0 ]; do
    case "$1" in
        --remove|remove|uninstall) do_remove ;;
        --tag) [ $# -ge 2 ] || { echo "--tag needs a value, e.g. --tag v1.0.0" >&2; exit 2; }; TAG="$2"; shift 2 ;;
        --tag=*) TAG="${1#--tag=}"; shift ;;
        *) echo "unknown option: $1 (use --tag vX.Y.Z or --remove)" >&2; exit 2 ;;
    esac
done

command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 1; }

target=$(detect_target)
if [ -n "$TAG" ]; then
    base="https://github.com/${REPO}/releases/download/${TAG}"
else
    base="https://github.com/${REPO}/releases/latest/download"
fi
asset="${BIN}-${target}"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "downloading ${asset} (${TAG:-latest})"
if ! curl -fSL "$base/$asset" -o "$tmp/$asset"; then
    echo "download failed: $base/$asset" >&2
    echo "check the tag exists: https://github.com/${REPO}/releases (GitHub may also be rate limiting this IP, retry later)" >&2
    exit 1
fi
curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS" || { echo "download failed: $base/SHA256SUMS" >&2; exit 1; }

want=$(awk -v f="$asset" '$2 == f {print $1}' "$tmp/SHA256SUMS")
[ -n "$want" ] || { echo "no checksum for $asset in SHA256SUMS" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
    got=$(sha256sum "$tmp/$asset" | awk '{print $1}')
else
    got=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
fi
[ "$want" = "$got" ] || { echo "checksum mismatch for $asset" >&2; exit 1; }
echo "checksum ok"

mkdir -p "$INSTALL_DIR"
staged="$INSTALL_DIR/.$BIN.new"
mv "$tmp/$asset" "$staged"
chmod 755 "$staged"
if [ "$OS" = "Darwin" ]; then
    xattr -d com.apple.quarantine "$staged" 2>/dev/null || true
fi
mv -f "$staged" "$INSTALL_DIR/$BIN"
echo "installed $INSTALL_DIR/$BIN"

case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *) echo "warning: $INSTALL_DIR is not in PATH - add it to your shell profile" ;;
esac

if ! "$INSTALL_DIR/$BIN" --version; then
    echo "warning: $BIN did not run on this system - check OS/CPU compatibility" >&2
    exit 1
fi
echo "done. run: $BIN --help"
