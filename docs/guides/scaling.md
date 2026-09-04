# Scaling the MCP Gateway

This guide covers scaling the MCP Gateway horizontally by running multiple replicas.

## Overview

By default, the MCP Gateway runs as a single replica. To handle increased traffic or improve availability, you can scale the gateway to multiple replicas behind Envoy, which load-balances requests across them.

Whether scaling requires an external session store depends on the MCP protocol version your clients and backends use:

- **2026-07-28** traffic is stateless and header-based. Every request carries its own routing intent, so any replica can serve any request with no shared state. Scale directly, with no `sessionStore`.
- **2025-11-25** traffic is stateful. The gateway router maintains in-memory session mappings between clients and backend MCP servers, so scaling requires an external Redis-based session store to make those mappings available to every replica.

Key concepts for stateful (2025-11-25) traffic:

- **Session Mapping**: Each gateway session ID maps to one or more backend MCP server session IDs
- **Lazy Initialization**: Backend sessions are created on first `tools/call`, not at connection time
- **Shared State**: An external Redis-based datastore makes session mappings accessible to all gateway replicas

## Do I need Redis?

Use this table to decide whether you need an external session store before scaling:

| Gateway traffic | Redis (`sessionStore`) | How to scale |
|-----------------|------------------------|--------------|
| **2026-07-28 only** — all clients and backends use 2026-07-28 | Not required | Scale replicas directly; stateless header routing means any replica serves any request. See [Scaling a 2026-07-28-only gateway](#scaling-a-2026-07-28-only-gateway-no-redis). |
| **2025-11-25 or mixed** — any 2025-11-25 client or backend | Required before scaling | Configure `sessionStore`, then scale. See [Scaling a 2025-11-25 or mixed gateway](#scaling-a-2025-11-25-or-mixed-gateway-redis-required). |

> **Note:** Scaling a 2025-11-25 or mixed gateway to multiple replicas **without** Redis breaks sessions across replicas. Session mappings live in each replica's memory, so when a client's request lands on a replica other than the one that created its backend session, that session is lost. Always configure `sessionStore` before scaling a gateway that serves any 2025-11-25 traffic.

## Prerequisites

- MCP Gateway installed and configured
- For 2025-11-25 or mixed gateways: a Redis-based datastore accessible from the gateway

The gateway connects using the Redis protocol and is compatible with any Redis-based datastore. For details on how to install and configure a datastore, see the documentation for your chosen implementation:

- [Redis documentation](https://redis.io/docs/latest/)
- [AWS ElastiCache (Redis OSS) User Guide](https://docs.aws.amazon.com/AmazonElastiCache/latest/red-ug/WhatIs.html)
- [Dragonfly documentation](https://www.dragonflydb.io/docs)
- [Valkey documentation](https://valkey.io/docs/)

## Scaling a 2026-07-28-only gateway (no Redis)

If all your clients and backends use the 2026-07-28 protocol, you do not need a session store. The 2026-07-28 router keeps no server-side sessions and no session mappings — each request carries its own routing intent in the `Mcp-Method` and `Mcp-Name` headers, so any replica can serve any request.

### Step 1: Verify No Session Store Configured

Confirm the MCPGatewayExtension has no `sessionStore` field:

```bash
kubectl get mcpgatewayextension mcp-gateway-extension -n mcp-system -o jsonpath='{.spec.sessionStore}'
```

The output should be empty. If it returns a value, Redis is already configured — the gateway is on the 2025-11-25 or mixed path, so follow [Scaling a 2025-11-25 or mixed gateway](#scaling-a-2025-11-25-or-mixed-gateway-redis-required) instead. This command only confirms whether a session store is configured; whether your traffic is 2026-07-28-only depends on the protocol versions your clients and backends negotiate, which you determine from the [Do I need Redis?](#do-i-need-redis) table above.

### Step 2: Scale the Deployment

Scale the gateway to the desired number of replicas:

```bash
kubectl scale deployment/mcp-gateway -n mcp-system --replicas=3
```

Verify all replicas are ready:

```bash
kubectl rollout status deployment/mcp-gateway -n mcp-system
```

Envoy load-balances requests across the replicas. There is no cross-replica state to share, so no shared session store and no `CACHE_CONNECTION_STRING` are required.

> **Note:** A replica passing its readiness probe does not guarantee it has already connected to every configured backend — readiness reports true once any upstream is ready or while backend config is still syncing. Envoy adds a replica to its load-balancing pool as soon as it is ready, so immediately after scaling a new replica may briefly return errors for tools whose backend it has not yet loaded. Allow a short warm-up after `kubectl rollout status` completes, or confirm a replica serves the expected tools by checking the broker `/status` endpoint before sending production traffic.

## Scaling a 2025-11-25 or mixed gateway (Redis required)

If any of your clients or backends use the 2025-11-25 protocol, configure an external Redis-based session store before scaling. The following steps deploy a datastore, wire it into the MCPGatewayExtension as `sessionStore`, and scale the gateway.

### Step 1: Deploy a Redis-based Datastore

If you don't already have a Redis-based datastore available, deploy one in your cluster. Any Redis-compatible deployment will work. For example, to deploy Redis:

```bash
kubectl apply -n your-namespace -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: redis
  labels:
    app: redis
spec:
  replicas: 1
  selector:
    matchLabels:
      app: redis
  template:
    metadata:
      labels:
        app: redis
    spec:
      containers:
        - name: redis
          image: mirror.gcr.io/redis:7-alpine
          ports:
            - containerPort: 6379
          readinessProbe:
            exec:
              command: ["redis-cli", "ping"]
            initialDelaySeconds: 5
            periodSeconds: 10
---
apiVersion: v1
kind: Service
metadata:
  name: redis
  labels:
    app: redis
spec:
  type: ClusterIP
  ports:
    - port: 6379
      targetPort: 6379
  selector:
    app: redis
EOF
```

Wait for the datastore to be ready:

```bash
kubectl rollout status deployment/redis -n your-namespace
```

### Step 2: Create the Redis Credentials Secret

Create a secret containing the Redis connection URL. The secret must have the `mcp.kuadrant.io/secret: "true"` label — without it, the MCPGatewayExtension will fail validation.

> **Note:** The secret must be created in the **same namespace as the MCPGatewayExtension**. The Redis deployment itself can run in any namespace — just ensure the connection string in the secret points to it correctly.

```bash
kubectl apply -n mcp-system -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: redis-credentials
  labels:
    mcp.kuadrant.io/secret: "true"  # required label
type: Opaque
stringData:
  CACHE_CONNECTION_STRING: "redis://redis.your-namespace.svc.cluster.local:6379"
EOF
```

**Connection String Format:**

```text
redis://<user>:<password>@<host>:<port>/<db>
```

For an instance without authentication in the same cluster, the host is typically `<service-name>.<namespace>.svc.cluster.local`.

### Step 3: Configure the MCPGatewayExtension

Add the `sessionStore` field to your MCPGatewayExtension to reference the Redis credentials secret:

```yaml
spec:
  sessionStore:
    secretName: redis-credentials
```

The operator will inject the Redis connection string as the `CACHE_CONNECTION_STRING` env var into the broker-router deployment.

### Step 4: Scale the Gateway

With the datastore configured, scale the gateway to multiple replicas:

```bash
kubectl scale deployment/mcp-gateway -n mcp-system --replicas=2
```

Verify all replicas are ready:

```bash
kubectl rollout status deployment/mcp-gateway -n mcp-system
```

### Step 5: Verify Session Sharing

Confirm that the external store is active by checking the gateway logs. You should see `cache using external redis store` on startup:

```bash
kubectl logs -n mcp-system deployment/mcp-gateway | grep "cache using external"
```

Test that sessions are shared across replicas by making multiple tool calls from the same client. The backend session ID should remain consistent regardless of which replica handles the request.

## Reverting to a Single Replica

To revert to in-memory session caching:

1. Remove the `sessionStore` field from your MCPGatewayExtension.

2. Scale down to a single replica:
   ```bash
   kubectl scale deployment/mcp-gateway -n mcp-system --replicas=1
   ```

3. Wait for the rollout to complete:
   ```bash
   kubectl rollout status deployment/mcp-gateway -n mcp-system
   ```

## Next Steps

With horizontal scaling configured, you can:
- **[Observability](./observability.md)** - Monitor gateway performance across replicas
- **[Troubleshooting](./troubleshooting.md)** - Debug session and routing issues
