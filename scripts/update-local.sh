#!/usr/bin/env bash
#
# update-local.sh — install the latest published herdr-goto release binary to
# the path herdr's config.toml points at (~/.config/herdr/goto-tui/goto).
#
# This is intentionally separate from scripts/release.sh: cutting a release and
# updating your own install are different actions. The keybind always runs the
# binary at that fixed path, so "use the latest release" == "drop the latest
# release binary there". To test local changes instead, build over the same
# path (see scripts/build-local.sh); run this to go back to the released build.
#
# Usage:
#   scripts/update-local.sh
#
set -euo pipefail

REPO="asumaran/herdr-goto"
DEST="${HERDR_GOTO_BIN:-$HOME/.config/herdr/goto-tui/goto}"

command -v gh >/dev/null 2>&1 || { echo "error: GitHub CLI (gh) is required." >&2; exit 1; }

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
asset="goto-${os}-${arch}"

echo "==> Resolving asset ${asset} from the latest release of ${REPO}..."
mkdir -p "$(dirname "$DEST")"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

if ! gh release download -R "$REPO" --pattern "$asset" --dir "$tmp" 2>/dev/null; then
  echo "error: latest release has no asset named '${asset}' (CI may still be building)." >&2
  exit 1
fi

install -m 0755 "$tmp/$asset" "$DEST"
echo "installed $("$DEST" -version 2>/dev/null || echo '?') -> $DEST"
