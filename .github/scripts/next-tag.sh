#!/usr/bin/env bash
# next-tag.sh — Determine the next release tag based on branch conventions.
#
# Branch rules:
#   main            → next minor (vX.Y+1.0) or major (vX+1.0.0 with --major)
#   release/X.Y.0   → next patch (vX.Y.Z+1)
#
# Usage:
#   next-tag.sh              # print the next tag
#   next-tag.sh --major      # bump major instead of minor (main only)
#   next-tag.sh --tag        # also create the git tag
#   next-tag.sh --push       # create tag and push (implies --tag)

set -euo pipefail

MAJOR=false
CREATE_TAG=false
PUSH=false

for arg in "$@"; do
  case "$arg" in
    --major) MAJOR=true ;;
    --tag)   CREATE_TAG=true ;;
    --push)  CREATE_TAG=true; PUSH=true ;;
    -h|--help)
      sed -n '2,/^$/s/^# //p' "$0"
      exit 0
      ;;
    *) echo "Unknown argument: $arg" >&2; exit 1 ;;
  esac
done

BRANCH="$(git rev-parse --abbrev-ref HEAD)"

# ---------------------------------------------------------------------------
# Pre-flight checks (only when creating a tag)
# ---------------------------------------------------------------------------
if [ "$CREATE_TAG" = true ]; then
  # Dirty working tree means uncommitted changes would not be in the release.
  if [ -n "$(git status --porcelain)" ]; then
    echo "ERROR: Working tree is dirty. Commit or stash changes before releasing." >&2
    exit 1
  fi

  # Ensure local branch is in sync with remote. Unpushed commits would
  # produce a tag that CI rejects ("not on main / release branch").
  git fetch origin "$BRANCH" --quiet 2>/dev/null || true
  LOCAL="$(git rev-parse HEAD)"
  REMOTE="$(git rev-parse "origin/$BRANCH" 2>/dev/null || true)"
  if [ -n "$REMOTE" ] && [ "$LOCAL" != "$REMOTE" ]; then
    AHEAD="$(git rev-list "origin/$BRANCH..HEAD" --count)"
    BEHIND="$(git rev-list "HEAD..origin/$BRANCH" --count)"
    if [ "$AHEAD" -gt 0 ]; then
      echo "ERROR: Local $BRANCH is $AHEAD commit(s) ahead of origin. Push first." >&2
    fi
    if [ "$BEHIND" -gt 0 ]; then
      echo "ERROR: Local $BRANCH is $BEHIND commit(s) behind origin. Pull first." >&2
    fi
    exit 1
  fi
fi

if [ "$BRANCH" = "main" ]; then
  # Find the latest tag by version to determine the next minor/major.
  LATEST_TAG="$(git tag --sort=-v:refname | head -1)"
  if [ -z "$LATEST_TAG" ]; then
    NEXT_TAG="v0.1.0"
  elif [[ "$LATEST_TAG" =~ ^v([0-9]+)\.([0-9]+)\.[0-9]+$ ]]; then
    CUR_MAJOR="${BASH_REMATCH[1]}"
    CUR_MINOR="${BASH_REMATCH[2]}"
    if [ "$MAJOR" = true ]; then
      NEXT_TAG="v$((CUR_MAJOR + 1)).0.0"
    else
      NEXT_TAG="v${CUR_MAJOR}.$((CUR_MINOR + 1)).0"
    fi
  else
    echo "ERROR: Cannot parse version from tag: $LATEST_TAG" >&2
    exit 1
  fi

elif [[ "$BRANCH" =~ ^release/([0-9]+)\.([0-9]+)\.0$ ]]; then
  REL_MAJOR="${BASH_REMATCH[1]}"
  REL_MINOR="${BASH_REMATCH[2]}"

  if [ "$MAJOR" = true ]; then
    echo "ERROR: --major is only valid on main" >&2
    exit 1
  fi

  # Find the latest patch tag in this release series.
  LATEST_PATCH="$(git tag --sort=-v:refname | grep -E "^v${REL_MAJOR}\.${REL_MINOR}\.[0-9]+$" | head -1)"
  if [ -z "$LATEST_PATCH" ]; then
    # No tags yet for this series — first patch after branch creation.
    NEXT_TAG="v${REL_MAJOR}.${REL_MINOR}.1"
  elif [[ "$LATEST_PATCH" =~ ^v[0-9]+\.[0-9]+\.([0-9]+)$ ]]; then
    NEXT_TAG="v${REL_MAJOR}.${REL_MINOR}.$((${BASH_REMATCH[1]} + 1))"
  else
    echo "ERROR: Cannot parse patch from tag: $LATEST_PATCH" >&2
    exit 1
  fi

else
  echo "ERROR: Releases must be cut from main or a release/X.Y.0 branch." >&2
  echo "  Current branch: $BRANCH" >&2
  exit 1
fi

echo "$NEXT_TAG"

if [ "$CREATE_TAG" = true ]; then
  git tag "$NEXT_TAG"
  echo "Created tag $NEXT_TAG" >&2
fi

if [ "$PUSH" = true ]; then
  git push origin "$NEXT_TAG"
  echo "Pushed tag $NEXT_TAG" >&2
fi
