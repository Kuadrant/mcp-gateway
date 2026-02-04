## Trusted Header Public Key Configuration

The MCP Broker can filter tools based on a signed JWT in the `x-authorized-tools` header. This enables identity-based tool filtering when integrated with an external authorization system.

### How It Works

1. An upstream authorization system validates the user's identity
2. It creates a signed JWT containing the user's allowed tools in an `allowed-tools` claim
3. This JWT is passed to the broker via the `x-authorized-tools` header
4. The broker validates the JWT signature and filters `tools/list` responses accordingly

### JWT Payload Format

The `allowed-tools` claim should contain a JSON object mapping server routes to tool arrays:

```json
{
  "allowed-tools": "{\"mcp-test/server1-route\":[\"tool_a\",\"tool_b\"],\"mcp-test/server2-route\":[\"tool_c\"]}",
  "exp": 1760004918,
  "iat": 1760004618
}
```

### Example Key Pair Generation

Generate an ECDSA P-256 key pair:

```bash
# Generate private key
openssl ecparam -name prime256v1 -genkey -noout -out private-key.pem

# Extract public key
openssl ec -in private-key.pem -pubout -out public-key.pem
```

### Create Kubernetes Secret

```bash
kubectl create secret generic trusted-headers-public-key \
  --from-file=key=public-key.pem \
  -n mcp-system
```

### Configure the Broker

Reference the secret in the broker deployment:

```yaml
env:
  - name: TRUSTED_HEADER_PUBLIC_KEY
    valueFrom:
      secretKeyRef:
        name: trusted-headers-public-key
        key: key
```

When this environment variable is set, the broker will validate any `x-authorized-tools` header using ES256 and filter the tools list accordingly. If validation fails, an empty tools list is returned.


### Example AuthPolicy that uses this method

An example AuthPolicy that implements the `x-authorized-tools` can be found at [Sample Tool Filtering](../../config/samples/oauth-token-exchange/tools-list-auth.yaml)
