# Session store supports Redis Sentinel and Cluster

## What changed

The session store Secret referenced by `MCPGatewayExtension.spec.sessionStore` can now select a
highly available Redis or Valkey backend. A new optional `CACHE_MODE` key selects `standalone`
(the default), `sentinel` or `cluster`:

| Mode | Keys |
|---|---|
| `standalone` | `CACHE_CONNECTION_STRING` (unchanged) |
| `sentinel` | `CACHE_SENTINEL_MASTER`, `CACHE_SENTINEL_ADDRS`, optional `CACHE_SENTINEL_USERNAME`, `CACHE_SENTINEL_PASSWORD` |
| `cluster` | `CACHE_CLUSTER_ADDRS` |

Sentinel and Cluster also accept optional `CACHE_USERNAME`, `CACHE_PASSWORD`, `CACHE_DB`
(Sentinel only, must be `0` for Cluster) and `CACHE_TLS_ENABLED`. The controller validates the
Secret for the selected mode and reports errors on the `MCPGatewayExtension` status. Keys that
the selected mode does not read are ignored, and the controller and broker-router log a warning
naming them.

The broker-router also accepts a new `--cache-mode` flag (env: `CACHE_MODE`).

With `CACHE_TLS_ENABLED`, server certificates must validate against the broker-router system
trust store. A custom CA for Redis is not yet supported.

## Who is affected

Installations that set `spec.sessionStore`. On upgrade, the controller adds the new `CACHE_*`
environment variables, all optional, to the controller-generated broker-router Deployment. This
changes the pod template, so those pods roll once. Installations without a session store are
unchanged.

## Migration Steps

No action needed. Existing Secrets with only `CACHE_CONNECTION_STRING` keep working in standalone
mode. To move to Sentinel, update the Secret, then restart the broker-router so it reads the new
values:

```bash
kubectl create secret generic redis-session-store -n mcp-system \
  --from-literal=CACHE_MODE=sentinel \
  --from-literal=CACHE_SENTINEL_MASTER=mymaster \
  --from-literal=CACHE_SENTINEL_ADDRS=sentinel-0.redis:26379,sentinel-1.redis:26379,sentinel-2.redis:26379 \
  --from-literal=CACHE_PASSWORD=<redis-password> \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl label secret redis-session-store -n mcp-system mcp.kuadrant.io/secret=true --overwrite
kubectl rollout restart deployment/mcp-gateway -n mcp-system
```

See the scaling guide for Cluster and TLS examples.
