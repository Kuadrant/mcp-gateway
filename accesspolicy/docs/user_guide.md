# XAccessPolicy User Guide

This guide explains how to use the `XAccessPolicy` custom resource to control access to your Model Context Protocol (MCP) servers using the `accesspolicy-controller`.

## Overview

The `XAccessPolicy` allows you to define declarative, tool-level access control for MCP servers running behind a Gateway. The policy uses Common Expression Language (CEL) to define rules. Our controller translates these high-level policies into Kuadrant `AuthPolicy` resources that are enforced at the data plane by Envoy and Authorino.

## Resource Structure

A standard `XAccessPolicy` contains two main sections: `targetRefs` and `rules`.

```yaml
apiVersion: agentic.networking.x-k8s.io/v1alpha1
kind: XAccessPolicy
metadata:
  name: example-policy
  namespace: my-namespace
spec:
  targetRefs:
    - group: gateway.networking.k8s.io
      kind: Gateway
      name: my-gateway
  action: Allow
  rules:
    - name: allow-specific-tool
      source:
        type: ServiceAccount
        serviceAccount:
          name: default
      authorization:
        type: Inline
        mcp:
          methods:
            - name: tools/call
              params:
                - search_web
```

### 1. `targetRefs`
Currently, the controller supports targeting `Gateway` resources. You must specify the `Gateway` that your MCP servers are exposed through.

*Note: In the future, this will be expanded to support targeting individual `XBackend` resources.*

### 2. `rules`
Rules define what traffic is allowed through the Gateway to your MCP servers.

- `name`: A descriptive name for the rule.
- `source`: Specifies the source identity of the request (e.g. a specific `ServiceAccount`).
- `authorization.type`: Usually set to `Inline` for standard MCP method matching, but can also be `CEL`.
- `authorization.mcp.methods`: A list of MCP methods to match (e.g. `tools/call` with specific `params`).

## The CEL Context

When writing CEL expressions in your `XAccessPolicy`, you have access to domain-specific MCP variables. 

### Supported Variables

Currently, the controller supports:
- `request.mcp.tool_name`: The name of the MCP tool the client is attempting to call.

*Example:*
```cel
request.mcp.tool_name == 'get-weather' || request.mcp.tool_name == 'get-time'
```

### How it Works (Under the Hood)
When the controller reconciles your policy, it translates inline MCP method matching into Authorino predicates such as `request.headers['x-mcp-toolname'] == 'search_web'`. The `mcp-gateway` (using an `ext_proc` sidecar) inspects the JSON-RPC payload of incoming requests, extracts the tool name, and injects it into the `x-mcp-toolname` HTTP header before passing the request to Authorino for evaluation.

## Policy Aggregation

Kuadrant (the underlying enforcement engine) strictly requires a 1:1 mapping between an `AuthPolicy` and a target network object. 

To simplify operations for developers, you can create **multiple** `XAccessPolicy` resources targeting the same `Gateway`. The `accesspolicy-controller` will automatically aggregate all rules from all `XAccessPolicies` targeting that Gateway and merge them into a single underlying `AuthPolicy`.

If *any* rule across all policies evaluates to `true`, the request is allowed (logical OR).

## Troubleshooting & Status

You can check the status of your policy using `kubectl describe xaccesspolicy <name>`.

The controller reports standard Kubernetes conditions:

1. **`Accepted`**: Indicates if your CEL expressions compiled successfully. If this is `False`, there is a syntax error in your CEL expression.
2. **`ResolvedRefs`**: Indicates if the target Gateway was successfully found in the cluster. If `False`, double-check the `targetRefs.name`.
3. **`Programmed`**: Indicates if the underlying `AuthPolicy` was successfully generated and pushed to the cluster.

### Example Status
```yaml
status:
  conditions:
  - type: Accepted
    status: "True"
    reason: Accepted
    message: "Valid CEL"
  - type: ResolvedRefs
    status: "True"
    reason: Resolved
    message: "Gateway found"
  - type: Programmed
    status: "True"
    reason: Programmed
    message: "AuthPolicy created"
```
