# Remove --max-request-body-size CLI Flag

The `--max-request-body-size` CLI flag on `mcp-broker-router` has been removed.
The body size limit is now configured via `MCPGatewayExtension.spec.maxBodyBytes`
(default: 5 MiB).

## What's Changed

- **`--max-request-body-size` removed**: the flag is no longer accepted by
  `mcp-broker-router`. Passing it will cause a startup error.
- **`spec.maxBodyBytes`**: use `MCPGatewayExtension.spec.maxBodyBytes` to
  configure a custom request body size limit.

## Migration

If your broker-router deployment passes `--max-request-body-size` in container
args or command, remove it and set `spec.maxBodyBytes` on your
MCPGatewayExtension instead:

```yaml
# Before
containers:
  - name: mcp-broker-router
    args:
      - --max-request-body-size=10485760  # remove this line
```

```yaml
# After
apiVersion: mcp.kuadrant.io/v1
kind: MCPGatewayExtension
spec:
  maxBodyBytes: 10485760
```
