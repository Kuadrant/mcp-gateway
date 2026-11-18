# Implementation Guide: CEL Validation & Translation

This document provides technical guidance for implementing the CEL validation and translation requirements outlined in the `XAccessPolicy` controller design doc.

## 1. CEL Macro Substitution
The translation layer relies on mapping abstract `XAccessPolicy` variables to the native context provided by `mcp-gateway` (Envoy/Authorino headers).

```go
package translator

import (
    "strings"
)

// TranslateCEL converts domain-specific MCP variables to the Envoy AuthZ header context
func TranslateCEL(expression string) string {
    // Replace abstract tool_name with the header injected by mcp-broker-router
    expr := strings.ReplaceAll(expression, "request.mcp.tool_name", "request.headers['x-mcp-toolname']")
    
    // Future proofing for prompts (out of scope for MVP, but follows the exact same pattern)
    // expr = strings.ReplaceAll(expr, "request.mcp.prompt_name", "request.headers['x-mcp-promptname']")
    
    return expr
}
```

## 2. CEL Syntax Compilation (Validation)
As per the design, we do not perform exhaustive semantic evaluation. We only guarantee that the expression is syntactically valid by compiling it against a mock environment.

### Adding Dependencies
```bash
go get github.com/google/cel-go/cel
```

### Implementing the Compiler
```go
package translator

import (
    "github.com/google/cel-go/cel"
    "github.com/google/cel-go/ext"
    "sync"
)

var (
    celEnv     *cel.Env
    celEnvErr  error
    celEnvOnce sync.Once
)

// getCelEnv initializes a CEL environment loosely mirroring the Envoy AuthZ context
func getCelEnv() (*cel.Env, error) {
    celEnvOnce.Do(func() {
        // Authorino's Envoy Context uses "request" as the base object
        celEnv, celEnvErr = cel.NewEnv(
            cel.Variable("request", cel.MapType(cel.StringType, cel.AnyType)),
            cel.Variable("auth", cel.MapType(cel.StringType, cel.AnyType)),
            ext.Strings(),
        )
    })
    return celEnv, celEnvErr
}

// ValidateCEL compiles the expression to verify syntactic correctness
func ValidateCEL(expression string) error {
    env, err := getCelEnv()
    if err != nil {
        return err // Internal error initializing environment
    }

    _, issues := env.Compile(expression)
    if issues != nil && issues.Err() != nil {
        return issues.Err() // Return syntax error for the controller to mark as InvalidCEL
    }

    return nil
}
```

## 3. Integrating with the Reconciler
In your reconciliation loop, combine translation and validation before creating the `AuthPolicy` predicate:

```go
translatedExpr := translator.TranslateCEL(rule.Authorization.CEL.Expression)

if err := translator.ValidateCEL(translatedExpr); err != nil {
    // Record the error in the XAccessPolicy status condition
    meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
        Type:    string(agenticv1alpha1.PolicyConditionAccepted),
        Status:  metav1.ConditionFalse,
        Reason:  string(agenticv1alpha1.PolicyReasonInvalidCEL),
        Message: fmt.Sprintf("CEL compilation failed: %v", err),
    })
    continue // Skip this rule
}

// If valid, append the translated expression to the combined predicates list
predicates = append(predicates, buildPredicate(translatedExpr))
```
