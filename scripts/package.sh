#!/usr/bin/env bash
# Build release artefacts into dist/:
#   overclaud-<v>-windows-amd64.exe / -arm64.exe   (OverclaudSetup.exe = amd64 copy)
#   overclaud-<v>-darwin-amd64 / -arm64            (CI turns these into Overclaud.pkg)
#   overclaud-<v>-linux-amd64 / -arm64, .tar.gz, .deb
#   install.sh, install.ps1, SHA256SUMS
#
#   scripts/package.sh <version>
#
# Code signing is not configured (decision 2026-10-21). To add it later: sign the .exe with
# Azure Trusted Signing after the Windows build, and pass --sign to pkgbuild plus run
# notarytool in the macOS CI job. Nothing else changes.
set -euo pipefail
v="${1:?version}"
here="$(cd "$(dirname "$0")/.." && pwd)"
dist="$here/dist"
mkdir -p "$dist"
find "$dist" -mindepth 1 -delete
export PATH="$HOME/.local/go/bin:$PATH"
ld="-s -w -X github.com/psumo/overclaud-installer/internal/app.Version=$v"

build() { # goos goarch out
  (cd "$here" && CGO_ENABLED=0 GOOS="$1" GOARCH="$2" go build -trimpath -ldflags "$ld" -o "$3" ./cmd/overclaud)
}

for arch in amd64 arm64; do
  build windows "$arch" "$dist/overclaud-$v-windows-$arch.exe"
  build darwin "$arch" "$dist/overclaud-$v-darwin-$arch"
  build linux "$arch" "$dist/overclaud-$v-linux-$arch"
  tar -C "$dist" -czf "$dist/overclaud-$v-linux-$arch.tar.gz" --transform "s|overclaud-$v-linux-$arch|overclaud|" "overclaud-$v-linux-$arch"

  # .deb: /usr/bin/overclaud; the user then runs `overclaud install` as themselves.
  pkg="$(mktemp -d)"
  mkdir -p "$pkg/DEBIAN" "$pkg/usr/bin"
  install -m 0755 "$dist/overclaud-$v-linux-$arch" "$pkg/usr/bin/overclaud"
  cat > "$pkg/DEBIAN/control" <<EOF
Package: overclaud
Version: $v
Section: devel
Priority: optional
Architecture: $arch
Maintainer: Overclaud <support@overclaud.dev>
Homepage: https://overclaud.dev
Description: Installer and updater for the Overclaud harness for Claude Code
 Run "overclaud install" as your normal user to install the harness into ~/.claude.
EOF
  cat > "$pkg/DEBIAN/postinst" <<'EOF'
#!/bin/sh
echo "Overclaud installer is ready. Run as your normal user (not root):  overclaud install"
EOF
  chmod 0755 "$pkg/DEBIAN/postinst"
  dpkg-deb --root-owner-group --build "$pkg" "$dist/overclaud_${v}_${arch}.deb" >/dev/null
  find "$pkg" -mindepth 1 -delete; rmdir "$pkg"
done
cp "$dist/overclaud-$v-windows-amd64.exe" "$dist/OverclaudSetup.exe"
cp "$here/packaging/install.sh" "$here/packaging/install.ps1" "$dist/"
(cd "$dist" && sha256sum -- * > SHA256SUMS)
ls -la "$dist"
