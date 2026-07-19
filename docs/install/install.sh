#!/bin/sh
# GoModel installer for macOS and Linux.
#
#   curl -fsSL https://gomodel.enterpilot.io/install.sh | sh
#
# Downloads the latest release binary from GitHub, verifies its SHA-256
# checksum, and installs it to /usr/local/bin (or ~/.local/bin when
# /usr/local/bin is not writable). No telemetry is sent by this script.
#
# Overrides:
#   GOMODEL_VERSION      install a specific version (e.g. v0.1.50); default: latest
#   GOMODEL_INSTALL_DIR  install directory; default: /usr/local/bin or ~/.local/bin

set -eu

REPO="ENTERPILOT/GoModel"
BINARY="gomodel"

say() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"

case "$(uname -s)" in
    Darwin) os="darwin" ;;
    Linux) os="linux" ;;
    *) fail "unsupported OS: $(uname -s) (Windows: use install.ps1)" ;;
esac

case "$(uname -m)" in
    x86_64 | amd64) arch="amd64" ;;
    arm64 | aarch64) arch="arm64" ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
esac

# Resolve the version from the /releases/latest redirect, avoiding the
# GitHub API and its per-IP rate limit.
tag="${GOMODEL_VERSION:-}"
if [ -z "$tag" ]; then
    location=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest") ||
        fail "could not resolve the latest release"
    tag="${location##*/}"
fi
case "$tag" in
    v*) ;;
    *) fail "unexpected release tag: $tag" ;;
esac
version="${tag#v}"

archive="${BINARY}_${version}_${os}_${arch}.tar.gz"
base_url="https://github.com/$REPO/releases/download/$tag"

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT INT TERM

say "Downloading $BINARY $tag ($os/$arch)..."
curl -fsSL -o "$tmpdir/$archive" "$base_url/$archive" || fail "download failed: $base_url/$archive"
curl -fsSL -o "$tmpdir/checksums.txt" "$base_url/checksums.txt" || fail "download failed: checksums.txt"

expected=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmpdir/checksums.txt")
[ -n "$expected" ] || fail "no checksum for $archive in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$tmpdir/$archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$tmpdir/$archive" | awk '{ print $1 }')
else
    fail "sha256sum or shasum is required to verify the download"
fi
[ "$actual" = "$expected" ] || fail "checksum mismatch for $archive"
say "Checksum verified."

tar -xzf "$tmpdir/$archive" -C "$tmpdir" "$BINARY"

install_dir="${GOMODEL_INSTALL_DIR:-}"
if [ -z "$install_dir" ]; then
    if [ -w /usr/local/bin ]; then
        install_dir="/usr/local/bin"
    else
        install_dir="$HOME/.local/bin"
    fi
fi
mkdir -p "$install_dir" || fail "cannot create $install_dir"
[ -w "$install_dir" ] || fail "$install_dir is not writable — set GOMODEL_INSTALL_DIR to a writable directory, or rerun with sudo"
install -m 755 "$tmpdir/$BINARY" "$install_dir/$BINARY"

say ""
say "Installed $BINARY $tag to $install_dir/$BINARY"
case ":$PATH:" in
    *":$install_dir:"*) ;;
    *)
        say ""
        say "Note: $install_dir is not on your PATH. Add it with:"
        say "  export PATH=\"$install_dir:\$PATH\""
        ;;
esac
say ""
say "Get started:"
say "  export OPENAI_API_KEY=sk-...   # or any other provider key"
say "  $BINARY"
say ""
say "Docs: https://gomodel.enterpilot.io"
