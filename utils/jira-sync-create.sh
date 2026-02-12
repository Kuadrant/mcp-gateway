#!/bin/bash
# Creates a Jira epic for a GitHub issue with bidirectional linking
# Usage: ./jira-sync-create.sh <GITHUB_NUM> <GITHUB_TITLE> <JIRA_FEATURE>
# Output: Created epic key

set -e

GITHUB_NUM="$1"
GITHUB_TITLE="$2"
JIRA_FEATURE="$3"

if [ -z "$GITHUB_NUM" ] || [ -z "$GITHUB_TITLE" ] || [ -z "$JIRA_FEATURE" ]; then
  echo "Usage: $0 <GITHUB_NUM> <GITHUB_TITLE> <JIRA_FEATURE>" >&2
  exit 1
fi

# Get bearer token
TOKEN=$(jira issue view CONNLINK-1 --debug 2>&1 | grep "Authorization: Bearer" | head -1 | awk '{print $3}')

if [ -z "$TOKEN" ]; then
  echo "Error: Could not get Jira bearer token" >&2
  exit 1
fi

# Create epic
EPIC_URL=$(jira epic create --project CONNLINK \
  -n "$GITHUB_TITLE" \
  -s "$GITHUB_TITLE" \
  --component "MCP Gateway" --label "mcp-gateway" --no-input \
  -b "GitHub: https://github.com/Kuadrant/mcp-gateway/issues/$GITHUB_NUM" 2>&1)

EPIC_KEY=$(echo "$EPIC_URL" | grep -oE 'CONNLINK-[0-9]+')

if [ -z "$EPIC_KEY" ]; then
  echo "Error: Failed to create epic" >&2
  echo "$EPIC_URL" >&2
  exit 1
fi

# Set Parent Link via REST API
curl -s -X PUT "https://issues.redhat.com/rest/api/2/issue/$EPIC_KEY" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"fields\":{\"customfield_12313140\":\"$JIRA_FEATURE\"}}"

# Add Jira link to GitHub issue
current_body=$(gh api repos/Kuadrant/mcp-gateway/issues/$GITHUB_NUM --jq '.body')
new_body="Jira: https://issues.redhat.com/browse/$EPIC_KEY

$current_body"
gh issue edit "$GITHUB_NUM" --repo Kuadrant/mcp-gateway --body "$new_body" >/dev/null

echo "$EPIC_KEY"
