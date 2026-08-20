# Breaking Change: `--gateway-ca-cert` flag removed

## What changed

The broker-router `--gateway-ca-cert` flag has been removed. Private-CA trust for the
2025-11-25 protocol hairpin client is now sourced from `MCPGatewayExtension.spec.caCertBundleRef`,
the same bundle already used as the broker's base trust pool for upstream MCP servers. The
controller writes it once to the config Secret as `gatewayCACertPEM`; the broker rebuilds the
hairpin client from that value on config change, with no deployment restart.

On the next reconcile the controller strips the now-defunct `--gateway-ca-cert` flag and the
matching `gateway-ca` volume and mount from the broker-router deployment. Stripping the flag is
required: the broker binary no longer defines it and would fail at flag parse if it were left in
place.

## Who is affected

Deployments that used an HTTPS gateway listener signed by a private CA with the **2025-11-25**
protocol, and that added `--gateway-ca-cert` by hand (per the pre-v1 docs). The 2026-07-28
protocol does not hairpin and was never affected.

## Migration Steps

1. Put the gateway listener CA in a labeled Secret in the `MCPGatewayExtension` namespace. If it
   differs from your upstream CAs, concatenate both PEM blocks into one Secret:

   ```bash
   cat /path/to/upstream-ca.pem /path/to/gateway-listener-ca.pem > /tmp/combined-ca.pem

   kubectl create secret generic shared-ca-bundle \
     --from-file=ca.crt=/tmp/combined-ca.pem \
     -n mcp-gateway

   kubectl label secret shared-ca-bundle \
     mcp.kuadrant.io/secret=true \
     -n mcp-gateway
   ```

2. Reference it from the `MCPGatewayExtension`:

   ```yaml
   spec:
     caCertBundleRef:
       name: shared-ca-bundle
       key: ca.crt   # optional, defaults to ca.crt
   ```

3. No manual cleanup of the old flag or volume is needed: the controller strips
   `--gateway-ca-cert` and the `gateway-ca` volume and mount on the next reconcile.

## Related: `caCertBundleRef` no longer forces upstreams to HTTPS

### What changed

Setting `caCertBundleRef` on the `MCPGatewayExtension` no longer upgrades upstream MCP
server URLs from `http://` to `https://`. The gateway CA bundle is now trust material only.
The upstream scheme is derived from the backend Service port: a port whose `appProtocol` or
`name` is `https` is treated as a TLS upstream. A per-server `caCertSecretRef` still marks its
upstream as HTTPS.

Previously (v0.8.0, v0.9.0) the mere presence of `caCertBundleRef` rewrote every upstream to
`https://`, which broke plain-HTTP backends sharing a gateway that also fronts an HTTPS listener.

### Who is affected

Deployments that set `caCertBundleRef` and relied on it alone to make an upstream HTTPS, where
that upstream's Service port is neither named `https` nor annotated with `appProtocol: https`,
and which do not set a per-server `caCertSecretRef`.

### Migration Steps

Mark the TLS upstream's Service port so the controller derives the `https` scheme:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-mcp-server
spec:
  ports:
    - name: https        # or set appProtocol: https
      port: 8443
      targetPort: 8443
```

Or set a per-server `caCertSecretRef` on the `MCPServerRegistration`, which marks the upstream
HTTPS and appends its CA to the trust pool.

See [Custom CA Certificates](../guides/custom-ca-certificates.md) for details.
