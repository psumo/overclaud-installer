#!/bin/sh
# Overclaud one-line installer for macOS and Linux:
#   curl -fsSL https://overclaud.dev/install | sh
# Downloads the latest overclaud binary from GitHub Releases, checks it against the
# release's SHA256SUMS, and runs `overclaud install`.
set -eu
repo="psumo/overclaud-installer"
base="https://github.com/$repo/releases/latest/download"

os="$(uname -s)"; arch="$(uname -m)"
case "$os" in Linux) os=linux ;; Darwin) os=darwin ;; *) echo "Unsupported OS: $os" >&2; exit 1 ;; esac
case "$arch" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) echo "Unsupported CPU: $arch" >&2; exit 1 ;; esac

command -v curl >/dev/null 2>&1 || { echo "curl is required" >&2; exit 1; }
tmp="$(mktemp -d)"
trap 'rm -f "$tmp/overclaud" "$tmp/SHA256SUMS"; rmdir "$tmp" 2>/dev/null || true' EXIT

curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS"
name="$(grep -E " overclaud-[0-9][^ ]*-$os-$arch\$" "$tmp/SHA256SUMS" | awk '{print $2}' | head -n1)"
[ -n "$name" ] || { echo "No installer for $os/$arch in the latest release" >&2; exit 1; }
want="$(grep -E " $name\$" "$tmp/SHA256SUMS" | awk '{print $1}')"
echo "Downloading $name"
curl -fsSL "$base/$name" -o "$tmp/overclaud"
if command -v sha256sum >/dev/null 2>&1; then got="$(sha256sum "$tmp/overclaud" | awk '{print $1}')"
else got="$(shasum -a 256 "$tmp/overclaud" | awk '{print $1}')"; fi
[ "$got" = "$want" ] || { echo "Checksum mismatch for $name; refusing to run it" >&2; exit 1; }
chmod +x "$tmp/overclaud"
# Read answers from the terminal even though this script came in on a pipe.
if [ -r /dev/tty ]; then exec "$tmp/overclaud" install "$@" </dev/tty; else exec "$tmp/overclaud" install "$@"; fi
