#!/bin/bash
# Lists Jira epics under a Feature with their GitHub issue links
# Usage: ./utils/jira-sync-epics.sh <JIRA_FEATURE>
# Output: TSV with columns: jira_key, summary, github_num

set -e

JIRA_FEATURE="$1"

if [ -z "$JIRA_FEATURE" ]; then
  echo "Usage: $0 <JIRA_FEATURE>" >&2
  exit 1
fi

# Get epics and extract GitHub issue numbers from descriptions
jira issue list --project CONNLINK \
  --jql "type = Epic AND 'Parent Link' = $JIRA_FEATURE" \
  --plain --columns key,summary 2>/dev/null | tail -n +2 | \
while IFS=$'\t' read -r key summary; do
  # Get description and extract GitHub issue number
  desc=$(jira issue view "$key" --raw 2>/dev/null | jq -r '.fields.description // ""')
  github_num=$(echo "$desc" | grep -oE 'github\.com/Kuadrant/mcp-gateway/issues/([0-9]+)' | grep -oE '[0-9]+$' | head -1)
  if [ -z "$github_num" ]; then
    github_num="none"
  fi
  printf "%s\t%s\t%s\n" "$key" "$summary" "$github_num"
done
