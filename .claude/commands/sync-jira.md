# GitHub-Jira Sync

Sync GitHub milestone issues with Jira Feature epics for MCP Gateway.

## Arguments

- `$ARGUMENTS` - Format: `<MILESTONE> <JIRA_FEATURE>` (e.g., `v0.6 OCPSTRAT-2798`)

## Instructions

Sync GitHub milestone issues to Jira epics under a Feature. Follow rules exactly.

### What Gets Synced

- GitHub Features (no parent) → Create Jira Epic
- GitHub Tasks (no parent) → Create Jira Epic

### What Does NOT Get Synced

- Bugs → Never sync
- Issues with a parent → Skip (parent's Epic covers them)
- Sub-issues → Stay in GitHub only

### Process

1. Parse arguments: `$ARGUMENTS` contains `<MILESTONE> <JIRA_FEATURE>`

2. Get bearer token:
```bash
TOKEN=$(jira issue view CONNLINK-1 --debug 2>&1 | grep "Authorization: Bearer" | head -1 | awk '{print $3}')
```

3. List GitHub issues in milestone:
```bash
gh issue list --repo Kuadrant/mcp-gateway --milestone "<MILESTONE>" --state all \
  --json number,title,state,labels \
  --jq '.[] | "\(.number)\t\(.state)\t\(.title)\t\([.labels[].name] | join(","))"'
```

4. For each issue, check if it has a parent using the GraphQL API:
```bash
gh api graphql -f query='
{
  repository(owner: "Kuadrant", name: "mcp-gateway") {
    issue(number: <NUMBER>) {
      parent {
        number
        title
      }
    }
  }
}' --jq '.data.repository.issue.parent.number // "none"'
```

If parent is not "none", skip the issue.

5. List existing Jira epics:
```bash
jira issue list --project CONNLINK \
  --jql "type = Epic AND 'Parent Link' = <JIRA_FEATURE>" \
  --plain --columns key,summary,status
```

6. Build table:

| GitHub # | Title | Parent | Jira Epic | Action |
|----------|-------|--------|-----------|--------|
| #XXX | Title | none | — | Create Epic |
| #YYY | Title | #ZZZ | — | Skip |
| #AAA | Title | none | CONNLINK-XXX | Exists |
| #BBB | Title | — | — | Skip (Bug) |

7. Create missing epics:
```bash
jira epic create --project CONNLINK \
  -n "<GitHub Issue Title>" \
  -s "<GitHub Issue Title>" \
  --component "MCP Gateway" --label "mcp-gateway" --no-input \
  -b "GitHub: https://github.com/Kuadrant/mcp-gateway/issues/<NUMBER>"
```

8. Set Parent Link via REST API:
```bash
curl -s -X PUT "https://issues.redhat.com/rest/api/2/issue/<NEW_EPIC_KEY>" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"fields":{"customfield_12313140":"<JIRA_FEATURE>"}}'
```

9. Verify:
```bash
jira issue view <EPIC_KEY> --raw | jq '{parentLink: .fields.customfield_12313140, featureLink: .fields.customfield_12318341.key}'
```

### CLI Limitations

| Issue | Workaround |
|-------|------------|
| `--parent` doesn't work cross-project | Use REST API curl |
| `--custom "Parent Link=XXX"` ignored | Use REST API curl |
| `jira issue create --type Epic` fails | Use `jira epic create -n` |
| Cannot delete issues | Close with "Won't Do" |

### Field Reference

| Field | ID | Set Via |
|-------|----|---------|
| Parent Link | customfield_12313140 | REST API |
| Feature Link | customfield_12318341 | Auto-populated |
| Epic Name | customfield_12311141 | `jira epic create -n` |
