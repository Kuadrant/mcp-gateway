# Lightweight Controller Design Doc: XAccessPolicy Controller

## Overview

### Purpose

The XAccessPolicy controller watches `XAccessPolicy` resources targeting `Gateway` objects and acts as a pluggable, gateway-aware translation layer. It translates gateway-agnostic MCP authorization rules into enforcement mechanisms specific to `mcp-gateway` (e.g., Envoy HTTP headers), ensuring a corresponding Kuadrant `AuthPolicy` exists to secure Model Context Protocol (MCP) servers on `kuadrant/mcp-gateway`.

Because Kuadrant’s `AuthPolicy` does not natively recognize `XAccessPolicy`'s domain-specific variables (like `request.mcp.tool_name`), the controller translates these custom variables into the gateway's native context variables (like `request.headers['x-mcp-toolname']`) using macro substitution, before embedding them into the `AuthPolicy`’s pattern matching predicates.

### Scope

- Watches `XAccessPolicy` resources
- Watches `Gateway` resources to ensure policy state reflects the underlying target
- Translates `XAccessPolicy` domain-specific variables via macro substitution
- Combines multiple `XAccessPolicies` targeting the same Gateway into a single Kuadrant `AuthPolicy`
- Validates Gateway target references
- Updates status conditions on `XAccessPolicy`

### Out of Scope

- Evaluation of authorization rules directly in the controller (Authorino handles this)
- Target resources other than `Gateway` (e.g., `XBackend` is out of scope for this MVP)
- Matching on MCP Prompts, Resources, or tool/prompt parameters. The initial prototype is strictly limited to Tool Name authorization.
- Multi-cluster support

---

## API

### Example Resource

```yaml
apiVersion: agentic.networking.x-k8s.io/v1alpha1
kind: XAccessPolicy
metadata:
  name: demo-access-policy
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: mcp-gateway
  rules:
    - name: allow-search-web-only
      authorization:
        type: CEL
        cel:
          expression: "request.mcp.tool_name == 'search_web'"
status:
  conditions:
  - type: Programmed
    status: "True"
```

### Relevant Fields

| Field | Purpose |
|-------|---------|
| `spec.targetRefs` | Specifies the target K8s object (must be `Gateway`). |
| `spec.rules[].authorization.cel.expression` | Defines the CEL-based authorization logic. |
| `status.conditions` | Standard K8s conditions representing controller progress. |

---

## Reconciliation Logic

### Inputs
- `XAccessPolicy`
- `Gateway` (to verify existence and handle modifications)
- `AuthPolicy` (to track existing state)

### Outputs
- Create/update Kuadrant `AuthPolicy`
- Update status of `XAccessPolicy`

### CEL Validation and Translation Strategy

The controller must translate domain-specific variables (like `request.mcp.tool_name`) into the data-plane equivalent (`request.headers['x-mcp-toolname']`) using macro substitution. 

Once translated, the controller will **only validate the syntactic correctness** of the CEL expression by ensuring it compiles using `cel-go`. 
The controller does **not** perform semantic validation or guarantee runtime success in the data plane. Since the control plane lacks runtime context, we accept any syntactically valid (compilable) expression. Runtime failures due to missing fields or logic errors are an accepted limitation, mitigated by documentation.

### Flow
1. Fetch all `XAccessPolicy` resources targeting a specific `Gateway`.
2. Inspect `targetRefs` to ensure it targets a `Gateway`.
3. Verify the referenced `Gateway` exists in the cluster.
4. Translate domain-specific variables in the `XAccessPolicy` CEL expressions using macro substitution. *Note: The header population is handled by the `mcp-gateway` itself (via `ext_proc`) prior to authorization.*
5. Compile the translated CEL expression. If compilation fails (syntax error), set `Accepted=False` with `InvalidCEL`. If it compiles, accept it.
6. Combine valid rules from multiple `XAccessPolicies` into a single, unified `AuthPolicy` to satisfy Kuadrant's 1:1 `AuthPolicy`-to-Target constraint.
7. Compare the desired `AuthPolicy` with the existing `AuthPolicy`.
8. Create/Update the `AuthPolicy` if needed via `CreateOrPatch`.
9. Update `XAccessPolicy` status conditions.

### Pseudocode

```go
func Reconcile() {
    policies := getXAccessPoliciesForGateway()
    if len(policies) == 0 {
        return
    }

    gateway := getGateway(policies[0].TargetRef)
    if gateway == nil {
        for _, policy := range policies {
            updateStatus(policy, ResolvedRefs_False, "GatewayNotFound")
        }
        return
    }

    var combinedPredicates []PatternMatchingPredicate
    
    for _, policy := range policies {
        if !isGatewayTarget(policy) {
            updateStatus(policy, Accepted_False, "InvalidTarget")
            continue
        }

        // Translate "request.mcp.tool_name" -> "request.headers['x-mcp-toolname']"
        translatedCEL := translateCEL(policy.Spec.Rules[0].Authorization.CEL.Expression)
        
        // Validate syntactic correctness (compilation)
        if err := compileCEL(translatedCEL); err != nil {
            updateStatus(policy, Accepted_False, "InvalidCEL")
            continue
        }

        combinedPredicates = append(combinedPredicates, buildPredicate(translatedCEL))
        
        updateStatus(policy, Programmed_True)
    }

    desiredAuthPolicy := buildCombinedAuthPolicy(gateway, combinedPredicates)
    reconcileAuthPolicy(desiredAuthPolicy)
}

func translateCEL(expression string) string {
    // Macro substitution converting abstract MCP logic to the target's data-plane reality
    return strings.ReplaceAll(expression, "request.mcp.tool_name", "request.headers['x-mcp-toolname']")
}
```

---

## Ownership

```text
XAccessPolicy (Multiple)
 └── AuthPolicy (Single Combined)
```

The generated `AuthPolicy` receives owner references to the `XAccessPolicy` resources it represents. This delegates garbage collection to Kubernetes.

---

## Reconcile Triggers

| Event | Action |
|-------|--------|
| `XAccessPolicy` created | Reconcile and combine policies for target |
| `XAccessPolicy` updated | Reconcile and combine policies for target |
| `AuthPolicy` modified | Reconcile (Revert manual drift) |
| `XAccessPolicy` deleted | Cleanup via K8s owner reference / Rebuild remaining |
| `Gateway` modified/deleted | Reconcile (Update `ResolvedRefs` condition) |

---

## Error Handling

| Error | Behavior |
|-------|----------|
| API conflict | Retry |
| Temporary API failure | Retry |
| Target Gateway not found | Set status condition `ResolvedRefs=False` |
| Invalid TargetRef kind | Set status condition `Accepted=False` |

---

## Status

```yaml
status:
  conditions:
  - type: Accepted
    status: "True"
  - type: ResolvedRefs
    status: "True"
  - type: Programmed
    status: "True"
```

---

## Testing

### Unit
- CEL macro translation logic (verifying `request.mcp.tool_name` is correctly substituted with `request.headers['x-mcp-toolname']`).
- Target validation and policy combination logic (merging rules from multiple policies).
- Status condition calculation.

### Integration
- Create `XAccessPolicy` → `AuthPolicy` created with translated CEL predicates.
- Create multiple `XAccessPolicies` targeting same `Gateway` → Single `AuthPolicy` successfully combines rules.
- Update `XAccessPolicy` CEL expressions → `AuthPolicy` predicates updated.
- Delete target `Gateway` → `XAccessPolicy` status updates to `ResolvedRefs=False`.
- (See `demo.md` for end-to-end integration flows using a local `mcp-gateway` environment.)
