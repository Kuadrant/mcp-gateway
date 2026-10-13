# AccessPolicy Controller: Implementation Task List

This document outlines the tasks needed to build the `XAccessPolicy`-to-`AuthPolicy` standalone controller. The focus is exclusively on `Gateway` targetRefs and a gateway-aware translation layer that maps abstract MCP authorization rules into Kuadrant's `AuthPolicy` by employing CEL macro substitution and combining rules.

## Phase 1: Controller Scaffolding
- [ ] **Task 1.1: Initialize Project**
  - Use `kubebuilder` to scaffold the project: `kubebuilder init --domain agentic.networking.x-k8s.io --repo github.com/your-org/accesspolicy-controller`.
  - Scaffold the `XAccessPolicy` API or import it from the upstream `kube-agentic-networking` repository.
- [ ] **Task 1.2: Dependencies & RBAC**
  - Add `github.com/kuadrant/kuadrant-operator/api/v1beta2` for the `AuthPolicy` CRD.
  - Add `github.com/google/cel-go/cel` for CEL syntax validation.
  - Add `+kubebuilder:rbac` markers for `GET/LIST/WATCH/UPDATE` on `XAccessPolicy` and `Gateway` resources.
  - Add `+kubebuilder:rbac` markers for `CREATE/UPDATE/PATCH/GET/LIST/WATCH/DELETE` on `AuthPolicy` resources.

## Phase 2: Translation and Validation Layer
- [ ] **Task 2.1: Implement CEL Macro Substitution**
  - Create a `translator` package.
  - Write a translation function `TranslateCEL(expr string) string` that uses `strings.ReplaceAll` to map `"request.mcp.tool_name"` to `"request.headers['x-mcp-toolname']"`.
- [ ] **Task 2.2: Implement CEL Syntax Validation**
  - Initialize a `cel.Env` inside the translator.
  - Write a validation function that runs `env.Compile(translatedCEL)`. If compilation fails, return an error so the reconciler can mark the policy as `InvalidCEL`.
- [ ] **Task 2.3: Implement Kuadrant Adapter**
  - Write logic to map the translated, validated CEL expressions into the `AuthPolicy` structure: `spec.rules.authorization["rule-name"].patternMatching.patterns[].predicate`.

## Phase 3: The Reconciler Logic
- [ ] **Task 3.1: Watch Configuration**
  - Setup the primary watch on `XAccessPolicy`.
  - Setup secondary watches:
    - On `AuthPolicy` with `Owns(&kuadrantv1beta2.AuthPolicy{})` to automatically revert manual drift by administrators.
    - On `Gateway` (using an event handler mapping) to trigger reconciles on policies when their target Gateway is modified or deleted.
- [ ] **Task 3.2: Reconcile Loop & Policy Combination**
  - Find all `XAccessPolicy` resources targeting the specific `Gateway`.
  - Validate that the target `Gateway` exists; if not, update status to `ResolvedRefs = False`.
  - Iterate through all policies targeting the Gateway, validating and translating their CEL expressions.
  - Combine all valid rules from these multiple policies into a single slice of predicates.
  - Apply the resulting combined `AuthPolicy` to the cluster using `controllerutil.CreateOrPatch`.
  - Update the status conditions of all successfully processed `XAccessPolicies` to `Programmed = True` or `Accepted = False` based on CEL validation.

## Phase 4: E2E Testing & Demo
- [ ] **Task 4.1: Unit Testing the Translator**
  - Write unit tests ensuring `request.mcp.tool_name` is correctly substituted.
  - Write unit tests verifying that syntactically invalid CEL is caught by the compiler step.
  - Write unit tests verifying that expressions with valid syntax but missing runtime fields (e.g. math errors, missing variables) are successfully compiled (accepted limitation).
- [ ] **Task 4.2: Local Testing Environment Setup**
  - Script a local `kind` cluster setup incorporating Gateway API, Kuadrant Operator, and `mcp-gateway`.
- [ ] **Task 4.3: E2E Integration Tests**
  - Assert that multiple `XAccessPolicies` targeting the same Gateway result in a **single** `AuthPolicy`.
  - Assert that applying an `XAccessPolicy` correctly programs the translated predicate.
  - Assert that deleting the target Gateway updates the policy's status to `ResolvedRefs=False`.
  - Assert that calling a forbidden tool via `curl` yields `403 Forbidden` from Authorino.
