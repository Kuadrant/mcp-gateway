# broker-router Deployment declares a named `metrics` port

## What changed

The controller-generated broker-router Deployment now declares a container port named
`metrics` (`9090/TCP`), the port the broker already serves Prometheus metrics on. A Prometheus
Operator `PodMonitor` with `port: metrics` now discovers one target per broker-router pod,
without relabeling. The broker-router `Service` is unchanged and does not expose port `9090`.

## Who is affected

All installations. On upgrade, the controller adds the port to the existing broker-router
Deployment. This changes the pod template, so the broker-router pods roll once. As with any
broker-router restart, 2025-11-25 sessions held in memory are lost unless a `sessionStore` is
configured, and affected clients must re-initialize.

While the old and new controller pods overlap during the upgrade, both reconcile the
Deployment for a few seconds. You may see its `metadata.generation` jump by several hundred and
an extra broker-router pod created and removed before it starts. This stops once the old
controller pod exits.

## Migration Steps

No action needed. Existing `PodMonitor`s that select the `http` port and relabel the address to
port `9090` keep working. You can optionally simplify them to select the `metrics` port directly:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: mcp-broker
  namespace: <namespace>
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: mcp-gateway
  podMetricsEndpoints:
    - port: metrics
      path: /metrics
```
