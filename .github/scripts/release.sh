#!/usr/bin/env bash
# release.sh — Release flow: generate release notes, review/edit, tag, push.
#
# Steps:
#   1. Generate release notes if they don't already exist
#   2. Open in $EDITOR for review and editing (interactive only)
#   3. Show final notes and prompt for confirmation (interactive only)
#   4. Commit and push the release notes
#   5. Tag and push the release
#
# Usage: ./scripts/release.sh [--no-interactive] <version>
#   e.g. .github/scripts/release.sh v0.7.0
#        .github/scripts/release.sh --no-interactive v0.7.0
#
# Non-interactive mode can also be enabled via INTERACTIVE=false env var.
# In non-interactive mode, release notes must already exist — the script
# will not generate them or open an editor.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Parse flags
INTERACTIVE="${INTERACTIVE:-true}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --no-interactive) INTERACTIVE=false; shift ;;
    -*) echo "Unknown flag: $1" >&2; exit 1 ;;
    *) break ;;
  esac
done

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 [--no-interactive] <version>" >&2
  exit 1
fi

TAG="$1"
RELEASE_NOTES_FILE="${PROJECT_ROOT}/docs/releasenotes/${TAG}.md"

echo "==> Releasing ${TAG}"
echo ""

# --- Step 1: Generate release notes if they don't exist ---

if [[ ! -f "$RELEASE_NOTES_FILE" ]]; then
  if [[ "$INTERACTIVE" == "false" ]]; then
    echo "ERROR: Release notes not found: docs/releasenotes/${TAG}.md" >&2
    echo "  In non-interactive mode, release notes must already exist." >&2
    echo "  Run 'make release-notes' first to generate and edit them." >&2
    exit 1
  fi
  RELEASE_TAG="$TAG" OUTPUT_FILE="$RELEASE_NOTES_FILE" \
    "${SCRIPT_DIR}/generate-release-notes.sh"
  echo ""
fi

if [[ "$INTERACTIVE" == "false" ]]; then
  # --- Non-interactive: show notes and proceed ---
  echo ""
  echo "──── Release Notes (${TAG}) ────"
  cat "$RELEASE_NOTES_FILE"
  echo "──── End Release Notes ────"
  echo ""
else
  # --- Step 2: Open for review/editing ---

  echo "==> Opening release notes for review..."
  echo "    File: docs/releasenotes/${TAG}.md"
  echo ""

  if [[ -n "${EDITOR:-}" ]]; then
    "$EDITOR" "$RELEASE_NOTES_FILE"
  elif command -v vim &>/dev/null; then
    vim "$RELEASE_NOTES_FILE"
  elif command -v nano &>/dev/null; then
    nano "$RELEASE_NOTES_FILE"
  else
    cat "$RELEASE_NOTES_FILE"
    echo ""
    echo "    No \$EDITOR found. Edit docs/releasenotes/${TAG}.md manually, then re-run."
    exit 1
  fi

  # --- Step 3: Show final notes and confirm ---

  echo ""
  echo "──── Release Notes (${TAG}) ────"
  cat "$RELEASE_NOTES_FILE"
  echo "──── End Release Notes ────"
  echo ""
  read -r -p "Proceed with release? [y/N] " REPLY
  if [[ ! "$REPLY" =~ ^[Yy]$ ]]; then
    echo "Aborted. Release notes saved at docs/releasenotes/${TAG}.md"
    echo "Edit and re-run 'make release' when ready."
    exit 0
  fi
fi

# --- Step 4: Commit and push release notes ---

cd "$PROJECT_ROOT"
git add "$RELEASE_NOTES_FILE"
if ! git diff --cached --quiet; then
  git commit -m "Add release notes for ${TAG}"
  git pull --rebase origin "$(git rev-parse --abbrev-ref HEAD)"
  git push
  echo "    Release notes committed and pushed."
else
  echo "    Release notes already committed."
fi

# --- Step 5: Pre-flight checks, tag, and push ---

BRANCH="$(git rev-parse --abbrev-ref HEAD)"

# Branch must be main or release/X.Y.0.
if [[ "$BRANCH" != "main" ]] && ! [[ "$BRANCH" =~ ^release/[0-9]+\.[0-9]+\.0$ ]]; then
  echo "ERROR: Releases must be cut from main or a release/X.Y.0 branch." >&2
  echo "  Current branch: $BRANCH" >&2
  exit 1
fi

# Dirty working tree means uncommitted changes would not be in the release.
if [[ -n "$(git status --porcelain)" ]]; then
  echo "ERROR: Working tree is dirty. Commit or stash changes before releasing." >&2
  exit 1
fi

# Ensure local branch is in sync with remote.
git fetch origin "$BRANCH" --quiet 2>/dev/null || true
LOCAL="$(git rev-parse HEAD)"
REMOTE="$(git rev-parse "origin/$BRANCH" 2>/dev/null || true)"
if [[ -n "$REMOTE" ]] && [[ "$LOCAL" != "$REMOTE" ]]; then
  AHEAD="$(git rev-list "origin/$BRANCH..HEAD" --count)"
  BEHIND="$(git rev-list "HEAD..origin/$BRANCH" --count)"
  if [[ "$AHEAD" -gt 0 ]]; then
    echo "ERROR: Local $BRANCH is $AHEAD commit(s) ahead of origin. Push first." >&2
  fi
  if [[ "$BEHIND" -gt 0 ]]; then
    echo "ERROR: Local $BRANCH is $BEHIND commit(s) behind origin. Pull first." >&2
  fi
  exit 1
fi

git tag "$TAG"
git push origin "$TAG"
echo ""
echo "==> Released ${TAG}"
