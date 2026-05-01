# Design Documents

## Structure

Each new design gets its own directory under `docs/design/`:

```text
docs/design/<feature-name>/
├── <feature-name>-design.md       # design document
└── tasks/
    ├── tasks.md                   # implementation plan with ordered tasks
    └── e2e_test_cases.md          # e2e test cases
```

Standalone design docs (e.g. `auth-phase-1.md`, `routing.md`) are older designs that predate this convention.

## Design Document

The design doc describes the problem, proposed solution, and key decisions. Structure:

- **Problem** — what's broken or missing
- **Summary** — one-paragraph description of the solution
- **Goals / Non-Goals** — scope boundaries
- **Job Stories** — user-centric scenarios that drive the design (see below)
- **Design** — the technical solution, containing:
  - **Prerequisites** — what must be in place before the feature can work
  - **Flow** — sequence diagrams showing the interaction between components
  - **Component Responsibilities** — table of which component does what
  - **API Changes** — CRD fields, config types, request/response schemas
  - **Data storage** — cache schemas, encryption, storage backends
- **Security Considerations** — threat model, auth requirements, data protection
- **Relationship to Existing Approaches** — how this fits alongside other solutions
- **Future Considerations** — what's explicitly deferred
- **Execution** — links to `tasks/tasks.md` and `tasks/e2e_test_cases.md` (don't list tasks inline)

Use mermaid diagrams for flows. Reference the MCP spec or other standards where applicable. Keep it focused on _what_ and _why_, not implementation steps.

### Job Stories

Job stories capture _when_ a situation arises, _what_ the actor wants, and _why_. They drive the design by grounding it in real user scenarios rather than abstract requirements. Format:

```text
### When <situation>

When <actor> <context>, they want <action/outcome> so that <benefit>.
```

Cover the key personas: platform operator, MCP client user, non-interactive agent. Include both the happy path and important edge cases (expired tokens, existing infrastructure, agents that can't use a browser).

## Implementation Plan (`tasks/tasks.md`)

The plan translates the design into ordered, dependency-aware tasks. Each task should have:

- **Task N: Title** (with Jira story reference)
- **Files** — which files to create or modify
- **Acceptance criteria** — checkboxes, testable conditions
- **Verification** — the `make` commands that prove it works

Start with existing code analysis — document what already exists that the implementation builds on. Order tasks by dependency so each can be implemented and verified independently.

## E2E Test Cases (`tasks/e2e_test_cases.md`)

Follow the format in `tests/e2e/test_cases.md`:

- Header format: `### [Tag1,Tag2] Description`
- Tags control test grouping and run frequency:
  - `Happy` — must pass every PR. Core flows and regression safety only.
  - Feature-specific tags (e.g. `URLElicitation`) — extended coverage: edge cases, error handling, configuration variants.
  - `Security` — security-focused tests: session validation, phishing prevention, access control.
- Tests can have multiple tags (e.g. `[Happy,URLElicitation]` or `[URLElicitation,Security]`). Not every test should be `Happy` — reserve it for the critical path so the tag remains meaningful.
- Single paragraph describing the scenario: given state → action → expected outcome
- Cover the happy path, error paths, edge cases, and regression safety (existing behavior unaffected)

## Documentation Plan (`tasks/documentation.md`)

Frame documentation around user goals, not features. Use job stories:

```text
### When I want to <goal>

When <actor> <context>, they want <action/outcome> so that <benefit>.

**Cover:**
- Key topics this section addresses
```

Organize by use cases rather than capabilities. Address underlying needs, not implementation details. Group by doc type (user guide, security architecture, API reference) with sections within each.

## Workflow

1. Write the design doc first — get agreement on approach before planning tasks
2. Create the implementation plan from the approved design
3. Define e2e test cases alongside the plan
4. Create GitHub sub-issues from the tasks, linked to the parent feature issue
5. Implement tasks in order, verifying each before moving to the next
