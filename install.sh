#!/bin/sh
# Installs the hindcast binary from GitHub releases.
#
#   curl -fsSL https://raw.githubusercontent.com/DushaneW/Hindcast/main/install.sh | sh
#
# HINDCAST_VERSION      release tag to install (default: latest)
# HINDCAST_INSTALL_DIR  target directory (default: ~/.local/bin)
set -eu

repo="DushaneW/Hindcast"
version="${HINDCAST_VERSION:-latest}"
install_dir="${HINDCAST_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
	echo "hindcast install: $*" >&2
	exit 1
}

case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "unsupported OS $(uname -s); on Windows use install.ps1" ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) fail "unsupported architecture $(uname -m)" ;;
esac

if [ -n "${HINDCAST_BASE_URL:-}" ]; then
	base="$HINDCAST_BASE_URL"
elif [ "$version" = latest ]; then
	base="https://github.com/$repo/releases/latest/download"
else
	base="https://github.com/$repo/releases/download/$version"
fi

fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$2" "$1"
	else
		fail "need curl or wget"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

archive="hindcast_${os}_${arch}.tar.gz"
tmp="$(mktemp -d)"
trap 'rm -r "$tmp"' EXIT

echo "downloading $archive ($version)"
fetch "$base/$archive" "$tmp/$archive" || fail "download failed: $base/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "download failed: $base/checksums.txt"

want="$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)"
[ -n "$want" ] || fail "$archive is not listed in checksums.txt"
[ "$(sha256 "$tmp/$archive")" = "$want" ] || fail "checksum mismatch for $archive"

tar -xzf "$tmp/$archive" -C "$tmp"
mkdir -p "$install_dir"
cp "$tmp/hindcast_${os}_${arch}/hindcast" "$install_dir/hindcast"
chmod 755 "$install_dir/hindcast"

echo "installed $("$install_dir/hindcast" version) to $install_dir/hindcast"
case ":$PATH:" in
*":$install_dir:"*) ;;
*) echo "add $install_dir to your PATH, for example: export PATH=\"$install_dir:\$PATH\"" ;;
esac
