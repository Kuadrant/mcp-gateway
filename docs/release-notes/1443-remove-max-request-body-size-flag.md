# Remove --max-request-body-size CLI Flag

The `--max-request-body-size` CLI flag on `mcp-broker-router` has been removed.
The body size limit is now a fixed 5 MiB default, matching the previous flag
default. It will be replaced by `MCPGatewayExtension.spec.maxBodyBytes` for
runtime configuration.

## What's Changed

- **`--max-request-body-size` removed**: the flag is no longer accepted by
  `mcp-broker-router`. Passing it will cause a startup error.
- **`MaxRequestBodySize` field removed** from `ExtProcServer`: the limit is now
  a package-level constant.

## Migration

If your broker-router deployment passes `--max-request-body-size` in container
args or command, remove it:

```yaml
# Before
containers:
  - name: mcp-broker-router
    args:
      - --max-request-body-size=10485760  # remove this line

# After
containers:
  - name: mcp-broker-router
    args: []
```

Once `spec.maxBodyBytes` is available on MCPGatewayExtension, use that to
configure a custom limit:

```yaml
apiVersion: mcp.kuadrant.io/v1
kind: MCPGatewayExtension
spec:
  maxBodyBytes: 10485760
```
