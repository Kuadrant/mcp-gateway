# Scaling the MCP Gateway

This guide covers scaling the MCP Gateway horizontally by running multiple replicas with shared session state.

## Overview

By default, the MCP Gateway runs as a single replica with session mappings stored in memory. To handle increased traffic or improve availability, you can scale the gateway to multiple replicas. However, because the gateway router maintains stateful session mappings between clients and backend MCP servers, scaling requires an external session store so that any replica can serve any client request.

Key concepts:
- **Session Mapping**: Each gateway session ID maps to one or more backend MCP server session IDs
- **Lazy Initialization**: Backend sessions are created on first `tools/call`, not at connection time
- **Shared State**: An external Redis-based datastore makes session mappings accessible to all gateway replicas

## Prerequisites

- MCP Gateway installed and configured
- A Redis-based datastore accessible from the gateway

The gateway connects using the Redis protocol and is compatible with any Redis-based datastore. For details on how to install and configure a datastore, see the documentation for your chosen implementation:

- [Redis documentation](https://redis.io/docs/latest/)
- [AWS ElastiCache (Redis OSS) User Guide](https://docs.aws.amazon.com/AmazonElastiCache/latest/red-ug/WhatIs.html)
- [Dragonfly documentation](https://www.dragonflydb.io/docs)
- [Valkey documentation](https://valkey.io/docs/)

## Step 1: Deploy a Redis-based Datastore

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
          image: redis:7-alpine
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

## Step 2: Create the Redis Credentials Secret

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

## Step 3: Configure the MCPGatewayExtension

Add the `sessionStore` field to your MCPGatewayExtension to reference the Redis credentials secret:

```yaml
spec:
  sessionStore:
    secretName: redis-credentials
```

The operator will inject the Redis connection string as the `CACHE_CONNECTION_STRING` env var into the broker-router deployment.

## Step 4: Scale the Gateway

With the datastore configured, scale the gateway to multiple replicas:

```bash
kubectl scale deployment/mcp-gateway -n mcp-system --replicas=2
```

Verify all replicas are ready:

```bash
kubectl rollout status deployment/mcp-gateway -n mcp-system
```

## Step 5: Verify Session Sharing

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
