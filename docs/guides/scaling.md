# Scaling the MCP Gateway

This guide covers scaling the MCP Gateway horizontally by running multiple replicas with shared session state.

## Overview

By default, the MCP Gateway runs as a single replica with session mappings stored in memory. To handle increased traffic or improve availability, you can scale the gateway to multiple replicas. However, because the gateway router maintains stateful session mappings between clients and backend MCP servers, scaling requires an external session store so that any replica can serve any client request.

Key concepts:
- **Session Mapping**: Each gateway session ID maps to one or more backend MCP server session IDs
- **Lazy Initialization**: Backend sessions are created on first `tools/call`, not at connection time
- **Shared State**: An external store (Redis) makes session mappings accessible to all gateway replicas

## Prerequisites

- MCP Gateway installed and configured
- A Redis instance accessible from the gateway (Redis 7+ recommended)

## Step 1: Deploy Redis

If you don't already have a Redis instance available, deploy one in your cluster. Any standard Redis deployment will work. For example:

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

Wait for Redis to be ready:

```bash
kubectl rollout status deployment/redis -n your-namespace
```

## Step 2: Configure the Gateway Connection

Configure the MCP Gateway to use Redis by setting the `CACHE_CONNECTION_STRING` environment variable on the gateway deployment:

```bash
kubectl set env deployment/mcp-gateway \
  CACHE_CONNECTION_STRING="redis://redis.your-namespace.svc.cluster.local:6379" \
  -n mcp-system
```

Wait for the rollout to complete:

```bash
kubectl rollout status deployment/mcp-gateway -n mcp-system
```

**Connection String Format:**

```
redis://<user>:<password>@<host>:<port>/<db>
```

For a Redis instance without authentication in the same cluster, the host is typically `redis.<namespace>.svc.cluster.local`.

> **Note:** The `CACHE_CONNECTION_STRING` environment variable maps to the `--cache-connection-string` CLI flag. Either can be used depending on your deployment method.

## Step 3: Scale the Gateway

With Redis configured, scale the gateway to multiple replicas:

```bash
kubectl scale deployment/mcp-gateway -n mcp-system --replicas=2
```

Verify all replicas are ready:

```bash
kubectl rollout status deployment/mcp-gateway -n mcp-system
```

## Step 4: Verify Session Sharing

Confirm that Redis is active by checking the gateway logs. You should see `session cache using external store` on startup:

```bash
kubectl logs -n mcp-system deployment/mcp-gateway | grep "session cache"
```

Test that sessions are shared across replicas by making multiple tool calls from the same client. The backend session ID should remain consistent regardless of which replica handles the request.

## Reverting to a Single Replica

To revert to in-memory session caching:

1. Scale down to a single replica:
   ```bash
   kubectl scale deployment/mcp-gateway -n mcp-system --replicas=1
   ```

2. Remove the environment variable:
   ```bash
   kubectl set env deployment/mcp-gateway CACHE_CONNECTION_STRING- -n mcp-system
   ```

3. Wait for the rollout to complete:
   ```bash
   kubectl rollout status deployment/mcp-gateway -n mcp-system
   ```

## Next Steps

With horizontal scaling configured, you can:
- **[Observability](./observability.md)** - Monitor gateway performance across replicas
- **[Troubleshooting](./troubleshooting.md)** - Debug session and routing issues
