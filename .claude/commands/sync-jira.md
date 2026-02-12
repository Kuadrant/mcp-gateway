# GitHub-Jira Sync

Sync GitHub milestone issues with Jira Feature epics.

## Arguments

- `$ARGUMENTS` - Format: `<MILESTONE> <JIRA_FEATURE>` (e.g., `v0.6 OCPSTRAT-2798`)

## Rules

**Sync:** GitHub Features/Tasks without a parent → Jira Epic

**Skip:** Bugs, issues with a parent, sub-issues

## Process

1. List GitHub issues:
```bash
./utils/jira-sync-list.sh <MILESTONE>
```
Output: `github_num, title, state, labels, parent`

2. List existing Jira epics:
```bash
./utils/jira-sync-epics.sh <JIRA_FEATURE>
```
Output: `jira_key, summary, github_num`

3. Match GitHub issues to Jira epics using the `github_num` column from epics output.

4. Build table. For each GitHub issue:
   - `parent != "none"` → Skip
   - Labels contain "bug" → Skip
   - Has matching Jira epic → Check bidirectional link
   - No matching epic, no parent, not bug → Create Epic

5. For new epics:
```bash
./utils/jira-sync-create.sh <GITHUB_NUM> "<TITLE>" <JIRA_FEATURE>
```

6. For missing GitHub→Jira links:
```bash
./utils/jira-sync-link.sh <GITHUB_NUM> <JIRA_KEY>
```

## Scripts

| Script | Purpose |
|--------|---------|
| `jira-sync-list.sh <MILESTONE>` | GitHub issues with parent info |
| `jira-sync-epics.sh <FEATURE>` | Jira epics with GitHub links |
| `jira-sync-create.sh <NUM> <TITLE> <FEATURE>` | Create epic + bidirectional links |
| `jira-sync-link.sh <NUM> <KEY>` | Add Jira link to GitHub issue |
