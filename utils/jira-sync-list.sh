#!/bin/bash
# Lists GitHub milestone issues with parent info
# Usage: ./utils/jira-sync-list.sh <MILESTONE>
# Output: TSV with columns: github_num, title, state, labels, parent

set -e

MILESTONE="$1"

if [ -z "$MILESTONE" ]; then
  echo "Usage: $0 <MILESTONE>" >&2
  exit 1
fi

# Get GitHub issues in milestone
gh issue list --repo Kuadrant/mcp-gateway --milestone "$MILESTONE" --state all --limit 100 \
  --json number,title,state,labels \
  --jq '.[] | [.number, .title, .state, ([.labels[].name] | join(","))] | @tsv' | \
while IFS=$'\t' read -r num title state labels; do
  # Check for parent
  parent=$(gh api graphql -f query="
  {
    repository(owner: \"Kuadrant\", name: \"mcp-gateway\") {
      issue(number: $num) {
        parent { number }
      }
    }
  }" --jq '.data.repository.issue.parent.number // "none"' 2>/dev/null)

  # Output TSV
  printf "%s\t%s\t%s\t%s\t%s\n" "$num" "$title" "$state" "$labels" "$parent"
done
