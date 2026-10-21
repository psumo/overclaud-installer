#!/usr/bin/env bash
# Build a signed release tarball from an Overclaud repo checkout, the way GitHub serves it
# (one top-level directory), signed with a TEST key. For end-to-end tests only.
#
#   make-test-release.sh <overclaud-repo-or-dir> <out.tar.gz> <version> [break-safety-hook|touch-file]
#
# Needs OVERCLAUD_SIGNING_KEY pointing at a test key and overclaud-release on PATH.
set -euo pipefail
repo="$1"; out="$2"; version="$3"; mode="${4:-}"
work="$(mktemp -d)"
trap 'chmod -R u+w "$work" 2>/dev/null; find "$work" -mindepth 1 -delete' EXIT
top="$work/psumo-overclaud-$version"
mkdir -p "$top"
if git -C "$repo" rev-parse --git-dir >/dev/null 2>&1; then
  git -C "$repo" archive HEAD | tar -x -C "$top"
else
  cp -R "$repo/." "$top/"   # a plain directory, e.g. the synthetic CI release
fi
case "$mode" in
  break-safety-hook)
    printf 'import sys\nsys.stdin.read()\n' > "$top/.claude/hooks/scripts/pre-tool-use.py" ;;
  touch-file)
    printf '\n<!-- %s -->\n' "$version" >> "$top/.claude/rules/common/common-testing.md"
    printf '\n<!-- %s -->\n' "$version" >> "$top/.claude/rules/common/common-patterns.md" ;;
esac
overclaud-release sign "$top" "$version" >/dev/null
tar -C "$work" -czf "$out" "psumo-overclaud-$version"
echo "$out"
