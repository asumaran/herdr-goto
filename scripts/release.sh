#!/usr/bin/env bash
#
# release.sh — cut a new herdr-goto release, gated on a clean tree and a green
# vet+build+test.
#
# Releases are created from a tag: a GitHub Actions workflow then compiles the
# binary (stamping the version via -ldflags) and attaches it as a release asset.
# This script makes sure we never tag a half-finished or broken state:
#
#   1. the working tree must be clean (the tag == exactly what is committed)
#   2. `go vet ./...`, `go build` and `go test ./...` must pass
#
# The CHANGELOG entry and the GitHub release notes are generated automatically
# from the commit subjects since the previous tag — nothing to write by hand.
#
# Usage:
#   scripts/release.sh 0.2.0            # release version 0.2.0
#   scripts/release.sh 0.2.0 --no-push  # do everything locally, skip push
#
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

DO_PUSH=true
VERSION=""
for arg in "$@"; do
  case "$arg" in
    --no-push) DO_PUSH=false ;;
    -h|--help) sed -n '2,19p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*)        echo "unknown argument: $arg" >&2; exit 2 ;;
    *)         VERSION="$arg" ;;
  esac
done

if [ -z "$VERSION" ]; then
  echo "error: version required, e.g. scripts/release.sh 0.2.0" >&2
  exit 2
fi
if ! printf '%s' "$VERSION" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "error: '$VERSION' is not a valid X.Y.Z version" >&2
  exit 2
fi

tag="v${VERSION}"

# --- preconditions ----------------------------------------------------------
if [ -n "$(git status --porcelain)" ]; then
  echo "error: working tree is dirty; commit or stash before releasing." >&2
  exit 1
fi
if git rev-parse -q --verify "refs/tags/${tag}" >/dev/null; then
  echo "error: tag ${tag} already exists." >&2
  exit 1
fi

# --- quality gate -----------------------------------------------------------
echo "==> go vet ./..."
go vet ./...
echo "==> go build ./..."
go build ./...
echo "==> go test ./..."
go test ./...

# --- generate changelog + release notes -------------------------------------
prev_tag="$(git tag --list 'v*' --sort=-version:refname | head -n1 || true)"
date_str="$(date +%Y-%m-%d)"

if [ -n "$prev_tag" ]; then
  log_range="${prev_tag}..HEAD"
  echo "==> Collecting commits ${prev_tag}..HEAD..."
else
  log_range="HEAD"
  echo "==> Collecting all commits (no previous tag)..."
fi

commits="$(git log --no-merges --pretty='format:* %s (%h)' "$log_range" \
  | grep -vE '^\* chore\(release\): ' || true)"
if [ -z "$commits" ]; then
  commits="* No changes since ${prev_tag:-the start}."
fi

# Prepend the new section to CHANGELOG.md.
{
  printf '## %s (%s)\n\n%s\n\n' "$tag" "$date_str" "$commits"
  cat CHANGELOG.md 2>/dev/null || true
} > CHANGELOG.md.tmp
mv CHANGELOG.md.tmp CHANGELOG.md

# Release notes: the same commit list plus a compare link to the previous tag.
origin_url="$(git config --get remote.origin.url || true)"
repo_slug="$(printf '%s' "$origin_url" | sed -E 's#^git@[^:]+:##; s#^https?://[^/]+/##; s#\.git$##')"
notes_file="$(mktemp)"
trap 'rm -f "$notes_file"' EXIT
printf '%s\n' "$commits" > "$notes_file"
if [ -n "$prev_tag" ] && [ -n "$repo_slug" ]; then
  printf '\n**Full Changelog**: https://github.com/%s/compare/%s...%s\n' \
    "$repo_slug" "$prev_tag" "$tag" >> "$notes_file"
fi

# --- apply ------------------------------------------------------------------
git add CHANGELOG.md
git commit -m "chore(release): ${tag}"
git tag "$tag"

if $DO_PUSH; then
  git push origin HEAD
  git push origin "$tag"
  # The build workflow triggers on a *published GitHub release*, not on a tag
  # push, so create the release. Notes come from the generated commit list.
  gh release create "$tag" --verify-tag --notes-file "$notes_file" --title "$tag"
  echo "released ${tag}: pushed branch + tag and published the GitHub release. CI will attach the binary."
else
  echo "released ${tag} locally (tag created, not pushed)."
  echo "The CHANGELOG entry is already committed; finish with:"
  echo "  git push origin HEAD && git push origin ${tag} && gh release create ${tag} --verify-tag --generate-notes --title ${tag}"
fi
