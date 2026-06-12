---
description: "Review PR changes against associated design plan and code quality"
argument-hint: "[PR-number-or-base-branch]"
---

# Review with Plan

Review the current branch's changes for code quality and — when a design plan exists — compare the implementation against the plan to catch drift.

**Input:** `$ARGUMENTS` — a PR number, PR URL, or base branch name. Defaults to `main` if not provided.

## Step 1: Gather changes

Determine the input type and collect the diff:

- **PR number or URL** — use `gh pr view <number> --json body,title,files` and `gh pr diff <number>` to get the PR description and full diff
- **Branch name or empty** — use `git diff $(git merge-base HEAD <base>)..HEAD` and `git log --oneline $(git merge-base HEAD <base>)..HEAD`

Read every changed file in full (not just the diff) so you understand surrounding context. If more than ~15 files changed, prioritize files that appear in the plan's task list, then files in hot paths (`internal/broker/`, `internal/mcp-router/`, `internal/controller/`), then the rest.

## Step 2: Find the associated plan

Search for a design plan connected to this PR. Try these in order:

1. **Changed files** — check if the diff touches anything under `docs/design/*/tasks/` or `docs/design/*.md`. If a subdirectory match, that design directory is the plan. If a flat file match (e.g. `docs/design/session-mgmt.md`), that file is the plan.
2. **PR description / commit messages** — scan for references to design docs (paths like `docs/design/...`, Jira story IDs, or feature names that match a `docs/design/<feature>/` directory).
3. **Feature area heuristic** — if the diff changes files in a specific feature area (e.g. `internal/broker/tokens.go`), check if a design directory exists for that feature (e.g. `docs/design/gateway-url-token-elicitation/`).

If a plan directory is found, read all files in it:
- The design doc (`*-design.md` or `*.md` at the root of the design directory)
- `tasks/tasks.md` — implementation plan with ordered tasks and acceptance criteria
- `tasks/e2e_test_cases.md` — expected e2e test cases
- `tasks/documentation.md` — documentation deliverables (if present)

If no plan is found, report "No associated plan found — proceeding with code-only review" and skip to Step 4.

## Step 3: Plan alignment review

This step is mandatory when a plan is found. Compare the implementation against the plan systematically.

### 3a. File and structure alignment

Compare files listed in the plan's tasks against what was actually created or modified:
- Files the plan says to create — do they exist? Are they named correctly?
- Files the plan says to modify — were they modified?
- Files created that the plan doesn't mention — are they justified?

### 3b. Acceptance criteria verification

For each task in the plan:
- Read every acceptance criterion (checkbox items)
- Verify each against the actual code — is it implemented? Is it implemented correctly?
- Flag criteria that are checked off in the plan but not actually met in the code
- Flag criteria that are unchecked but appear to be implemented

### 3c. API and naming alignment

Compare specific names and values between plan and code:
- CRD field names, config struct fields, type names
- Flag names and defaults
- Endpoint paths (e.g. `/tokens` vs `/credentials`)
- Cache key prefixes and storage field names
- Error codes and error messages
- Environment variable names

### 3d. E2E test case coverage

If `e2e_test_cases.md` exists:
- List each test case from the plan
- Check if a corresponding test exists in `tests/e2e/`
- Flag missing tests
- Flag tests that exist but don't match the described scenario

### 3e. Documentation deliverables

If `documentation.md` exists:
- Check each documented deliverable (guide sections, API reference updates, security architecture updates)
- Verify the deliverable exists and covers what the plan specifies
- Flag gaps between planned documentation and actual documentation

### Plan alignment output

```
## Plan Alignment

**Plan:** <path to design directory>

### Drift Summary
| Area | Plan | Actual | Status |
|------|------|--------|--------|
| ... | ... | ... | Match / Drift / Missing |

### Details
[For each drift or missing item, explain what the plan says vs what the code does]

### Uncovered Acceptance Criteria
[List any acceptance criteria not met by the implementation]

### Missing E2E Tests
[List test cases from the plan with no corresponding test]

### Missing Documentation
[List documentation deliverables not yet written]
```

## Step 4: Review tests first

Tests reveal intent and coverage gaps before you read implementation code. Review all test files in the diff before any production code.

For each test file:
- Does the test cover the feature's use cases and acceptance criteria (from the plan if one exists)?
- Are edge cases tested (empty input, error paths, boundary values, concurrent access)?
- Do tests verify behavior, not implementation details?
- Are test names descriptive enough to serve as documentation?
- Bug fixes must include a regression test — flag if missing
- Table-driven tests for multiple cases, no `time.Sleep`

If the tests don't adequately cover the feature, flag this as a blocking issue before proceeding. Incomplete test coverage is the highest-priority finding after plan drift.

## Step 5: Review implementation

### Preferred: Use agent-skills

If the `agent-skills:code-review-and-quality` skill is available, invoke it to perform the implementation review. It covers five axes: correctness, readability, architecture, security, and performance.

### Fallback: Focused Go review

If agent-skills is not available, review for: concurrency safety (goroutine shutdown paths, shared state protection), Go idioms (error wrapping, interfaces, context propagation), security (input validation, no logged secrets), maintainability (single responsibility, no dead code), and performance (allocations in hot paths).

### Code review output

```
## Code Review

### Summary
One paragraph: what this PR does and whether it is ready to merge.

### Verdict
One of: **APPROVE**, **REQUEST CHANGES**, or **COMMENT**

### Issues
Numbered list. Each issue:
- **Severity**: `blocking` or `nit`
- **File:line**: `path/to/file.go:42`
- **Category**: which review area
- **Description**: what is wrong and why
- **Suggestion**: concrete fix
```

## Step 6: Final output

Combine all sections (Plan Alignment + Test Review + Code Review) into a single structured review. Priority order: plan drift first, then test coverage gaps, then implementation issues.

If there is no plan, output the Test Review and Code Review sections.
