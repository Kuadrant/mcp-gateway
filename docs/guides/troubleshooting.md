# Troubleshooting MCP Gateway

This guide covers common issues and solutions when working with MCP Gateway across installation, configuration, and operation.

## Installation Issues

### Helm Installation Fails

**Symptom**: `helm install` command fails or times out

```bash
# Check Helm repository access
helm repo list

# Verify connectivity to GitHub Container Registry
curl -I https://ghcr.io

# Check cluster connectivity
kubectl cluster-info
```

**Solutions**:
- Ensure you have network access to `ghcr.io`
- Verify your cluster is running and accessible
- Check that Gateway API CRDs are installed: `kubectl get crd gateways.gateway.networking.k8s.io`
- Ensure Istio is installed: `kubectl get pods -n istio-system`

### Kustomize Installation Fails

**Symptom**: `kubectl apply -k` fails with validation errors

```bash
# Verify Gateway API CRDs exist
kubectl get crd gateways.gateway.networking.k8s.io httproutes.gateway.networking.k8s.io

# Check for resource conflicts
kubectl get mcpsr -A
kubectl get deployment -n mcp-system
```

**Solutions**:
- Install Gateway API CRDs first: `kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml`
- Delete existing resources if upgrading: `kubectl delete -k 'https://github.com/Kuadrant/mcp-gateway/config/install?ref=main'`

### Pods Not Starting

**Symptom**: MCP Gateway pods stuck in `Pending`, `CrashLoopBackOff`, or `ImagePullBackOff`

```bash
# Check pod status
kubectl get pods -n mcp-system

# Describe problem pods
kubectl describe pod -n mcp-system <pod-name>

# Check logs
kubectl logs -n mcp-system <pod-name>
```

**Common Causes**:
- **ImagePullBackOff**: Check image repository access and credentials
- **CrashLoopBackOff**: Check logs for application errors
- **Pending**: Check resource availability and node capacity
- **Init Container Failures**: Check RBAC permissions

## Gateway Routing Issues

### Gateway Listener Not Working

**Symptom**: Cannot reach MCP endpoint at configured hostname

```bash
# Check Gateway status
kubectl get gateway -A
kubectl describe gateway <gateway-name> -n <namespace>

# Verify listener configuration
kubectl get gateway <gateway-name> -n <namespace> -o yaml | grep -A 10 listeners
```

**Solutions**:
- Ensure Gateway has `Accepted` and `Programmed` conditions set to `True`
- Verify hostname in listener matches your DNS/hosts configuration
- Check that Istio gateway pod is running: `kubectl get pods -n gateway-system -l istio=ingressgateway`
- Verify port is not already in use: `kubectl get gateway -A -o yaml | grep "port:"`

### HTTPRoute Not Attached

**Symptom**: HTTPRoute exists but traffic doesn't reach the backend

```bash
# Check HTTPRoute status
kubectl get httproute -A
kubectl describe httproute <route-name> -n <namespace>

# Verify parent reference
kubectl get httproute <route-name> -n <namespace> -o yaml | grep -A 5 parentRefs
```

**Solutions**:
- Ensure `parentRefs` matches your Gateway name and namespace exactly
- Verify `hostnames` in HTTPRoute matches Gateway listener hostname
- Check that `allowedRoutes.namespaces` in Gateway allows HTTPRoute namespace
- Look for `Accepted` condition in HTTPRoute status

### EnvoyFilter Not Applied

**Symptom**: MCP requests fail or bypass the router

```bash
# Check EnvoyFilter exists
kubectl get envoyfilter -n istio-system

# Verify EnvoyFilter configuration
kubectl describe envoyfilter mcp-ext-proc -n istio-system

# Check Istio gateway pod configuration
kubectl exec -n gateway-system deploy/mcp-gateway-istio -- curl localhost:15000/config_dump | grep ext_proc
```

**Solutions**:
- Ensure EnvoyFilter is in `istio-system` namespace
- Verify `workloadSelector` matches Istio gateway labels: `kubectl get pods -n gateway-system --show-labels`
- Check port number matches Gateway listener port (default: 8080)
- Verify broker service name and namespace in `grpc_service` configuration
- Restart Istio gateway to force config reload: `kubectl rollout restart deployment/mcp-gateway-istio -n gateway-system`

## MCPGatewayExtension Issues

### MCPGatewayExtension Not Ready

**Symptom**: MCPGatewayExtension shows `Ready: False` status

```bash
# Check MCPGatewayExtension status
kubectl get mcpgatewayextension -A
kubectl describe mcpgatewayextension <name> -n <namespace>
```

**Common Reasons**:

- **ReferenceGrantRequired**: The MCPGatewayExtension targets a Gateway in a different namespace but no ReferenceGrant exists
- **InvalidMCPGatewayExtension**: The target Gateway doesn't exist, or another MCPGatewayExtension already targets this Gateway

**Solutions**:
- For cross-namespace references, create a ReferenceGrant in the Gateway's namespace:
  ```bash
  kubectl apply -f - <<EOF
  apiVersion: gateway.networking.k8s.io/v1beta1
  kind: ReferenceGrant
  metadata:
    name: allow-mcp-extension
    namespace: <gateway-namespace>
  spec:
    from:
      - group: mcp.kagenti.com
        kind: MCPGatewayExtension
        namespace: <mcpgatewayextension-namespace>
    to:
      - group: gateway.networking.k8s.io
        kind: Gateway
  EOF
  ```
- Verify the target Gateway exists: `kubectl get gateway -n <gateway-namespace>`
- Check for conflicting MCPGatewayExtensions: `kubectl get mcpgatewayextension -A`

### MCPServerRegistration Shows NotReady - No Valid MCPGatewayExtension

**Symptom**: MCPServerRegistration has condition `Ready: False` with message about no valid MCPGatewayExtension

```bash
kubectl describe mcpsr <name> -n <namespace>
```

**Solutions**:
- Verify an MCPGatewayExtension exists in the same namespace as the MCPServerRegistration
- Ensure the MCPGatewayExtension targets the Gateway that the HTTPRoute is attached to
- Check the MCPGatewayExtension is in Ready state: `kubectl get mcpgatewayextension -n <namespace>`

## MCP Server Configuration Issues

### MCP Server Not Discovered

**Symptom**: Tools from MCP server don't appear in `tools/list`

```bash
# Check MCPServerRegistration resource status
kubectl get mcpsr -A
kubectl describe mcpserver <server-name> -n <namespace>

# Check controller logs
kubectl logs -n mcp-system -l app=mcp-controller | grep <server-name>

# Check broker logs
kubectl logs -n mcp-system -l app=mcp-broker-router | grep "Discovered tools"
```

**Solutions**:
- Verify MCPServerRegistration `targetRef` points to correct HTTPRoute name and namespace
- Ensure HTTPRoute has `mcp-server: 'true'` label
- Check that backend MCP server is running: `kubectl get pods -n <mcp-server-namespace>`
- Verify backend service exists: `kubectl get svc -n <namespace> <service-name>`
- Check HTTPRoute has valid backend reference: `kubectl describe httproute <route-name>`

### Tools Not Appearing

**Symptom**: MCPServerRegistration discovered but tools missing

```bash
# Test backend server directly
# NOTE: You may need a valid mcp-session-id header set
kubectl run -it --rm debug --image=nicolaka/netshoot --restart=Never -- \
  curl -X POST http://<service-name>.<namespace>.svc.cluster.local:<port>/mcp \
  -H "mcp-session-id: SESSION_ID" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}'

# Check broker router logs for errors
kubectl logs -n mcp-system -l app=mcp-broker-router
```

**Solutions**:
- Verify backend MCP server implements `tools/list` method correctly
- Check backend server logs for errors
- Ensure backend server returns valid MCP protocol responses
- Verify `toolPrefix` in MCPServerRegistration spec is valid (no spaces or special chars)

### Tool Prefix Not Applied

**Symptom**: Tools appear without the configured prefix

```bash
# Check MCPServerRegistration configuration
kubectl get mcpsr <server-name> -n <namespace> -o yaml | grep toolPrefix

# Check controller logs
kubectl logs -n mcp-system deployment/mcp-gateway-controller | grep prefix
```

**Solutions**:
- Ensure `toolPrefix` is set in MCPServerRegistration spec
- Verify no typos in `toolPrefix` field name
- Restart broker after MCPServerRegistration changes: `kubectl rollout restart deployment/mcp-gateway-broker-router -n mcp-system`

## External MCP Server Issues

### Cannot Connect to External Server

**Symptom**: External MCP server tools not appearing or connection errors

```bash
# Check ServiceEntry
kubectl get serviceentry -n <namespace>
kubectl describe serviceentry <name> -n <namespace>

# Check DestinationRule
kubectl get destinationrule -n <namespace>
kubectl describe destinationrule <name> -n <namespace>

# Test DNS resolution
kubectl run -it --rm debug --image=nicolaka/netshoot --restart=Never -- \
  nslookup <external-hostname>

# Test external connectivity
kubectl run -it --rm debug --image=nicolaka/netshoot --restart=Never -- \
  curl -v https://<external-hostname>
```

**Solutions**:
- Verify ServiceEntry `hosts` matches external hostname exactly
- Ensure DestinationRule `host` matches ServiceEntry host
- Check network egress policies allow external traffic
- Verify HTTPRoute uses Hostname backendRef with correct external hostname
- Check TLS configuration in DestinationRule (mode, SNI)
- Ensure HTTPRoute has URLRewrite filter to rewrite to external hostname

### External Server Authentication Failing

**Symptom**: External server returns 401/403 errors

```bash
# Check secret exists and has correct label
kubectl get secret <secret-name> -n <namespace> --show-labels

# Verify secret contents
kubectl get secret <secret-name> -n <namespace> -o yaml

# Check MCPServerRegistration credentialRef
kubectl get mcpsr <name> -n <namespace> -o yaml | grep -A 3 credentialRef
```

**Solutions**:
- Ensure secret has label `mcp.kagenti.com/credential: "true"`
- Verify secret data key matches `credentialRef.key` in MCPServerRegistration
- Check credential format (e.g., "Bearer TOKEN" for GitHub)
- Verify credential has necessary permissions for the external service
- Check broker logs for credential errors: `kubectl logs -n mcp-system deployment/mcp-gateway-broker-router | grep -i auth`

## Authentication Issues

### OAuth Discovery Not Working

**Symptom**: Clients cannot discover OAuth configuration

```bash
# Test protected resource metadata endpoint
curl http://<mcp-hostname>/.well-known/oauth-protected-resource

# Check broker environment variables
kubectl get deployment mcp-gateway-broker-router -n mcp-system -o yaml | grep -A 10 env
```

**Solutions**:
- Verify `OAUTH_*` environment variables are set on broker deployment
- Ensure HTTPRoute includes path for `/.well-known/oauth-protected-resource`
- Check that `/.well-known` paths are excluded from authentication policy
- Verify broker pod restarted after environment variable changes

### JWT Token Validation Failing

**Symptom**: Valid tokens rejected with 401 errors

```bash
# Check AuthPolicy configuration
kubectl get authpolicy -A
kubectl describe authpolicy <policy-name> -n <namespace>

# Check Authorino logs
kubectl logs -n kuadrant-system -l authorino-resource=authorino

# Decode JWT to verify claims
echo "<your-token>" | cut -d. -f2 | base64 -d | jq
```

**Solutions**:
- Verify `issuerUrl` in AuthPolicy matches Keycloak realm
- Ensure issuer URL is reachable from cluster (use cluster-local service name)
- Check token expiration time (`exp` claim)
- Verify audience (`aud` claim) if required
- Ensure token includes required claims (groups, email, etc.)

### WWW-Authenticate Header Missing

**Symptom**: 401 responses don't include OAuth discovery information

```bash
# Test with verbose output
curl -v http://<mcp-hostname>/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}'
```

**Solutions**:
- Verify AuthPolicy includes `response.unauthenticated.headers.WWW-Authenticate`
- Check that response value includes correct metadata URL
- Ensure AuthPolicy is applied to correct Gateway/listener

## Authorization Issues

### All Tools Denied (403)

**Symptom**: Authenticated user gets 403 for all tool calls

```bash
# Check AuthPolicy authorization rules
kubectl get authpolicy <policy-name> -n <namespace> -o yaml | grep -A 20 authorization

# Check Authorino logs for CEL evaluation
kubectl logs -n kuadrant-system -l authorino-resource=authorino | grep -i authz
```

**Solutions**:
- Ensure Authorino can communicate with the Keycloak server
- Verify JWT token includes `resource_access[server-name].roles` claims

### Authorization Policy Not Applied

**Symptom**: Authorization checks not enforced

```bash
# Check AuthPolicy status
kubectl describe authpolicy <policy-name> -n <namespace>

# Verify policy targets correct resource
kubectl get authpolicy <policy-name> -n <namespace> -o yaml | grep -A 5 targetRef
```

**Solutions**:
- Ensure AuthPolicy `targetRef` matches Gateway name and namespace
- Verify `sectionName` matches Gateway listener name
- Check that Kuadrant operator is running: `kubectl get pods -n kuadrant-system`
- Look for `Accepted` condition in AuthPolicy status

### CEL Expression Errors

**Symptom**: Authorization fails with CEL evaluation errors

```bash
# Check Authorino logs for CEL errors
kubectl logs -n kuadrant-system -l authorino-resource=authorino | grep -i cel
```

**Solutions**:
- Verify CEL syntax in authorization rules
- Check that referenced fields exist (e.g., `auth.identity.groups`)
- Ensure metadata source is accessible and returns expected structure
- Test CEL expression syntax using online validators
- Add logging to understand CEL evaluation context

## Virtual MCP Server Issues

### Virtual Server Not Filtering Tools

**Symptom**: All tools returned even with virtual server header

```bash
# Check MCPVirtualServer resource
kubectl get mcpvirtualserver -A
kubectl describe mcpvirtualserver <name> -n <namespace>

# Test with virtual server header
curl -X POST http://<mcp-hostname>/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: <session-id>" \
  -H "X-Mcp-Virtualserver: <namespace>/<name>" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}' | jq '.result.tools[].name'
```

**Solutions**:
- Verify `X-Mcp-Virtualserver` header format is `namespace/name`
- Ensure virtual server name and namespace match exactly (case-sensitive)
- Check that tool names in virtual server spec match actual tool names
- Verify session was initialized with same virtual server header
- Check broker logs for virtual server processing

### Virtual Server Tools Not Found

**Symptom**: Virtual server returns empty tool list

```bash
# List all available tools
curl -X POST http://<mcp-hostname>/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: <session-id>" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}' | jq '.result.tools[].name'

# Compare with virtual server tool list
kubectl get mcpvirtualserver <name> -n <namespace> -o yaml | grep -A 20 tools
```

**Solutions**:
- Ensure tool names in virtual server spec match exactly (including prefix)
- Check for typos in tool names
- Verify tools exist in underlying MCP servers
- Update virtual server spec with correct tool names

## Session Management Issues

### Session ID Not Returned

**Symptom**: `initialize` response doesn't include `mcp-session-id` header

```bash
# Test initialization with header dump
curl -D - -X POST http://<mcp-hostname>/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "test", "version": "1.0"}}}'
```

**Solutions**:
- Verify broker is handling MCP protocol correctly
- Check broker logs for initialization errors
- Ensure EnvoyFilter is properly routing to external processor
- Test with `-D -` flag to dump response headers

### Session State Lost

**Symptom**: Subsequent requests fail with "session not found"

```bash
# Check broker session storage
kubectl logs -n mcp-system -l app=mcp-broker-router | grep -i session
```

**Solutions**:
- Ensure `mcp-session-id` header is included in subsequent requests
- Verify session hasn't expired (default timeout varies)
- Check if broker pod restarted (loses in-memory sessions)
- Consider implementing persistent session storage for production

## General Debugging

### Enable Debug Logging

```bash
# Increase log verbosity for controller (adjust deployment name as needed)
kubectl set env deployment/mcp-gateway-controller LOG_LEVEL=debug -n mcp-system

# Increase log verbosity for broker (adjust deployment name as needed)
kubectl set env deployment/mcp-gateway-broker-router LOG_LEVEL=debug -n mcp-system

# Check Istio proxy logs (adjust deployment name as needed)
kubectl logs -n gateway-system deploy/mcp-gateway-istio -c istio-proxy
```

### Check Component Health

```bash
# Check all MCP Gateway components
kubectl get pods -n mcp-system
kubectl get deploy -n mcp-system

# Check resource status
kubectl get mcpsr -A
kubectl get mcpvirtualserver -A
kubectl get authpolicy -A

# Check Gateway API resources
kubectl get gateway -A
kubectl get httproute -A
```

### Network Connectivity Testing

```bash
# Test broker from within cluster
kubectl run -it --rm test --image=curlimages/curl --restart=Never -- \
  curl -v http://mcp-gateway-broker.mcp-system.svc.cluster.local:8080/health
```

## Getting Help

If you continue to experience issues:

1. Collect logs from all components:
   ```bash
   kubectl logs -n mcp-system -l app=mcp-controller > controller.log
   kubectl logs -n mcp-system -l app=mcp-broker-router > broker.log
   kubectl get mcpsr -A -o yaml > mcpservers.yaml
   kubectl get httproute -A -o yaml > httproutes.yaml
   kubectl get gateway -A -o yaml > gateways.yaml
   ```

2. Check resource status:
   ```bash
   kubectl describe mcpserver -A > mcpserver-status.txt
   kubectl describe gateway -A > gateway-status.txt
   kubectl describe httproute -A > httproute-status.txt
   ```

3. Open an issue at https://github.com/Kuadrant/mcp-gateway/issues with:
   - Description of the problem
   - Steps to reproduce
   - Relevant logs and resource configurations
   - Kubernetes and Istio versions
