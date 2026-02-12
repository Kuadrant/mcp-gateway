#!/bin/bash
# Adds Jira link to GitHub issue if not already present
# Usage: ./jira-sync-link.sh <GITHUB_NUM> <JIRA_KEY>
# Output: "added" or "exists"

set -e

GITHUB_NUM="$1"
JIRA_KEY="$2"

if [ -z "$GITHUB_NUM" ] || [ -z "$JIRA_KEY" ]; then
  echo "Usage: $0 <GITHUB_NUM> <JIRA_KEY>" >&2
  exit 1
fi

# Check if link already exists
current_body=$(gh api repos/Kuadrant/mcp-gateway/issues/$GITHUB_NUM --jq '.body')
if echo "$current_body" | grep -q "$JIRA_KEY"; then
  echo "exists"
  exit 0
fi

# Add link
new_body="Jira: https://issues.redhat.com/browse/$JIRA_KEY

$current_body"
gh issue edit "$GITHUB_NUM" --repo Kuadrant/mcp-gateway --body "$new_body" >/dev/null
echo "added"
