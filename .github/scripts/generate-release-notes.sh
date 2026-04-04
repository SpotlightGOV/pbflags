#!/usr/bin/env bash
# generate-release-notes.sh — Use the Claude API to produce user-facing release
# notes from the git log between the two most recent tags (or between a tag and
# HEAD). The output is written to a file suitable for goreleaser --release-notes.
#
# Required env:
#   ANTHROPIC_API_KEY   — Claude API key
#   GITHUB_TOKEN        — GitHub token for fetching PR descriptions
#   GITHUB_REPOSITORY   — owner/repo (set automatically in Actions)
#
# Optional env:
#   RELEASE_TAG         — tag being released (default: latest tag)
#   PREVIOUS_TAG        — previous tag to diff against (default: second-latest)
#   OUTPUT_FILE         — path to write notes (default: release-notes.md)
#   CLAUDE_MODEL        — model to use (default: claude-sonnet-4-20250514)

set -euo pipefail

OUTPUT_FILE="${OUTPUT_FILE:-release-notes.md}"
CLAUDE_MODEL="${CLAUDE_MODEL:-claude-sonnet-4-20250514}"

# ---------------------------------------------------------------------------
# Resolve tags
# ---------------------------------------------------------------------------
if [ -z "${RELEASE_TAG:-}" ]; then
  RELEASE_TAG="$(git describe --tags --abbrev=0 2>/dev/null || true)"
  if [ -z "$RELEASE_TAG" ]; then
    echo "ERROR: No tags found. Cannot generate release notes." >&2
    exit 1
  fi
fi

if [ -z "${PREVIOUS_TAG:-}" ]; then
  PREVIOUS_TAG="$(git describe --tags --abbrev=0 "${RELEASE_TAG}^" 2>/dev/null || true)"
fi

if [ -n "$PREVIOUS_TAG" ]; then
  RANGE="${PREVIOUS_TAG}..${RELEASE_TAG}"
  echo "Generating release notes for ${RANGE}"
else
  RANGE="${RELEASE_TAG}"
  echo "Generating release notes for all commits up to ${RELEASE_TAG} (no previous tag)"
fi

# ---------------------------------------------------------------------------
# Collect commit log
# ---------------------------------------------------------------------------
COMMIT_LOG="$(git log "${RANGE}" --pretty=format:'%h %s' --no-merges 2>/dev/null || true)"
if [ -z "$COMMIT_LOG" ]; then
  COMMIT_LOG="$(git log "${RANGE}" --pretty=format:'%h %s' 2>/dev/null || true)"
fi

if [ -z "$COMMIT_LOG" ]; then
  echo "No commits found in range ${RANGE}. Writing empty notes." >&2
  echo "# ${RELEASE_TAG}" > "$OUTPUT_FILE"
  exit 0
fi

# ---------------------------------------------------------------------------
# Collect merged PR numbers and their descriptions via GitHub API
# ---------------------------------------------------------------------------
PR_CONTEXT=""
if [ -n "${GITHUB_TOKEN:-}" ] && [ -n "${GITHUB_REPOSITORY:-}" ]; then
  # Extract PR numbers from merge commit messages or (#NNN) references
  PR_NUMBERS="$(git log "${RANGE}" --pretty=format:'%s' | grep -oP '#\K[0-9]+' | sort -un || true)"

  for pr in $PR_NUMBERS; do
    PR_JSON="$(curl -sf \
      -H "Authorization: token ${GITHUB_TOKEN}" \
      -H "Accept: application/vnd.github+json" \
      "https://api.github.com/repos/${GITHUB_REPOSITORY}/pulls/${pr}" 2>/dev/null || true)"
    if [ -n "$PR_JSON" ]; then
      PR_TITLE="$(echo "$PR_JSON" | jq -r '.title // empty')"
      PR_BODY="$(echo "$PR_JSON" | jq -r '.body // empty' | head -50)"
      PR_LABELS="$(echo "$PR_JSON" | jq -r '[.labels[].name] | join(", ")' 2>/dev/null || true)"
      if [ -n "$PR_TITLE" ]; then
        PR_CONTEXT="${PR_CONTEXT}
--- PR #${pr}: ${PR_TITLE} ---
Labels: ${PR_LABELS}
${PR_BODY}
"
      fi
    fi
  done
fi

# ---------------------------------------------------------------------------
# Build the prompt
# ---------------------------------------------------------------------------
PROMPT="You are writing release notes for pbflags version ${RELEASE_TAG}.

pbflags is a feature-flag management system built on Protocol Buffers. It includes:
- A gRPC server for flag evaluation
- A protoc code-generation plugin (protoc-gen-pbflags)
- A sync tool for pushing flag definitions to the server
- Client libraries (Go, Java)

Below are the commits and PR descriptions between ${PREVIOUS_TAG:-'the beginning'} and ${RELEASE_TAG}.

## Commits
${COMMIT_LOG}

## Pull Request Details
${PR_CONTEXT:-'(No PR details available)'}

## Instructions

Write concise, user-facing release notes in Markdown. Follow these rules:
1. Group changes by THEME (e.g., \"Server\", \"Code Generation\", \"Client Libraries\", \"Infrastructure\"), not by conventional-commit category.
2. Highlight breaking changes at the top in a dedicated \"Breaking Changes\" section (only if there are any).
3. Highlight new capabilities and improvements users will notice.
4. Include a \"Migration Steps\" section if any changes require user action (only if needed).
5. Use bullet points. Each bullet should be a single clear sentence.
6. Do NOT list every commit — synthesize related changes into coherent bullets.
7. Do NOT include commit hashes or PR numbers in the output.
8. Keep the total length under 60 lines.
9. Start with a one-sentence summary of the release theme.
10. Use a level-2 heading (##) for each section."

# ---------------------------------------------------------------------------
# Call Claude API
# ---------------------------------------------------------------------------
if [ -z "${ANTHROPIC_API_KEY:-}" ]; then
  echo "WARNING: ANTHROPIC_API_KEY not set. Falling back to commit list." >&2
  {
    echo "# ${RELEASE_TAG}"
    echo ""
    echo "## Changes"
    echo ""
    echo "$COMMIT_LOG" | sed 's/^/- /'
  } > "$OUTPUT_FILE"
  exit 0
fi

# Escape the prompt for JSON
ESCAPED_PROMPT="$(echo "$PROMPT" | jq -Rs .)"

RESPONSE="$(curl -sf --max-time 60 \
  -H "x-api-key: ${ANTHROPIC_API_KEY}" \
  -H "anthropic-version: 2023-06-01" \
  -H "content-type: application/json" \
  -d "{
    \"model\": \"${CLAUDE_MODEL}\",
    \"max_tokens\": 2048,
    \"messages\": [{\"role\": \"user\", \"content\": ${ESCAPED_PROMPT}}]
  }" \
  "https://api.anthropic.com/v1/messages" 2>/dev/null || true)"

if [ -z "$RESPONSE" ]; then
  echo "WARNING: Claude API call failed. Falling back to commit list." >&2
  {
    echo "# ${RELEASE_TAG}"
    echo ""
    echo "## Changes"
    echo ""
    echo "$COMMIT_LOG" | sed 's/^/- /'
  } > "$OUTPUT_FILE"
  exit 0
fi

# Extract the text content from the response
NOTES="$(echo "$RESPONSE" | jq -r '.content[0].text // empty')"

if [ -z "$NOTES" ]; then
  # Check for error
  ERROR="$(echo "$RESPONSE" | jq -r '.error.message // empty')"
  if [ -n "$ERROR" ]; then
    echo "WARNING: Claude API error: ${ERROR}. Falling back to commit list." >&2
  else
    echo "WARNING: Empty response from Claude API. Falling back to commit list." >&2
  fi
  {
    echo "# ${RELEASE_TAG}"
    echo ""
    echo "## Changes"
    echo ""
    echo "$COMMIT_LOG" | sed 's/^/- /'
  } > "$OUTPUT_FILE"
  exit 0
fi

echo "$NOTES" > "$OUTPUT_FILE"
echo "Release notes written to ${OUTPUT_FILE}"
