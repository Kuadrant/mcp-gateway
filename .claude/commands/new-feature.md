# New Feature Development

Guide for starting and completing a new feature with consistent quality standards.

## Phase 1: Gather Context

### Step 1: Request design document

Ask the user for a link to the design document or issue describing this feature. If provided:
- Fetch the document using WebFetch or gh CLI
- Summarize the key requirements and acceptance criteria
- Identify any open questions or ambiguities

### Step 2: Create design document if none exists

If no design document exists, gather information from the user and create one:

1. Ask the user to describe:
   - What the feature should accomplish
   - Expected inputs/outputs or user interactions
   - Any constraints or dependencies

2. Create a design document at `docs/design/<feature-name>.md` with this structure:

```markdown
# Feature: <Feature Name>

## Summary
Brief description of what this feature does and why it's needed.

## Goals
- Goal 1
- Goal 2

## Non-Goals
- What this feature explicitly does not address

## Design

### Architecture Changes
Describe any changes to the system architecture.

### API Changes
List any new or modified CRDs, fields, or annotations.

### Component Changes
Which components are affected (broker, router, controller).

## Implementation Plan
1. Step 1
2. Step 2
3. Step 3

## Testing Strategy
- Unit tests: what to test
- E2E tests: what scenarios to validate

## Open Questions
- Any unresolved decisions

## Execution

### Todo
- [ ] Task 1
- [ ] Task 2
- [ ] Task 3

### Completed
- [x] Completed task (move here when done)
```

3. Review the design document with the user before proceeding

### Step 4: Maintain execution todos

As implementation proceeds:
- Add new todos to the design document as they are identified
- Move completed items from "Todo" to "Completed" section
- Keep the design doc as the source of truth for feature progress
- Update the doc whenever scope changes or new work is discovered

### Step 3: Understand scope

Based on the design document:
- Identify which components will be affected (broker, router, controller, CRDs, etc.)
- List the files likely to be modified or created
- Note any new dependencies or API changes

## Phase 2: Implementation Guidelines

Throughout implementation, maintain these priorities:

### Code Quality Standards

1. **Simplicity over cleverness**: Prefer straightforward, readable code
2. **Minimal changes**: Only modify what's necessary for the feature
3. **No speculative features**: Implement what's requested, not what might be needed
4. **Consistent style**: Match existing patterns in the codebase

### Comments

- Only add comments where logic isn't self-evident
- Keep comments terse and lowercase
- Update existing comments if behavior changes
- Remove outdated comments

### Testing

- Write unit tests for new logic
- Update existing tests if behavior changes
- Ensure tests validate actual requirements, not implementation details
- Run `make test-unit` frequently during development

## Phase 3: Wrap-up Checklist

Before considering the feature complete, perform these checks:

### 1. Comment review

Scan modified files for:
- Outdated comments that no longer reflect the code
- Missing comments where complex logic needs explanation
- Over-commenting of obvious code (remove these)

### 2. Test evaluation

Review all tests added or modified:
- Does each test validate a real requirement?
- Are there redundant tests that can be consolidated?
- Are edge cases covered appropriately?
- Run: `make test-unit`

### 3. Unused code detection

Search for:
- Functions or methods that are no longer called
- Variables that are assigned but never used
- Imports that are no longer needed
- Dead code paths from refactoring

Run: `make lint`

### 4. Helm chart sync

If CRDs, deployments, or RBAC changed:
- Run `/sync-chart` to verify helm templates are up to date
- Check values.yaml for any new configuration options needed

### 5. Documentation

If the feature adds user-facing functionality:
- Update CLAUDE.md if architecture or development workflow changed
- Update relevant docs in docs/ directory if needed

### 6. Final validation

```bash
make lint
make test-unit
make test-e2e  # if infrastructure is available
```

## Design Document Reference

$ARGUMENTS

If no argument provided, ask the user for the design document link or feature description.
