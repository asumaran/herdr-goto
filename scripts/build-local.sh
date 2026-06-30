#!/usr/bin/env bash
#
# build-local.sh — compile the current source over the path the herdr keybind
# runs (~/.config/herdr/goto-tui/goto), so you can test uncommitted changes.
#
# This overwrites whatever released binary update-local.sh installed there. To
# go back to the latest published release, run scripts/update-local.sh.
#
# Usage:
#   scripts/build-local.sh
#
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

DEST="${HERDR_GOTO_BIN:-$HOME/.config/herdr/goto-tui/goto}"
ver="local-$(git rev-parse --short HEAD 2>/dev/null || echo dev)"

mkdir -p "$(dirname "$DEST")"
go build -ldflags "-X main.version=${ver}" -o "$DEST" .
echo "built ${ver} -> $DEST"
