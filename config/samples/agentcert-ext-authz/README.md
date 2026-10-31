# AgentCert verification via ext_authz (decoupled, shadow-mode)

A minimal, self-contained example of verifying an agent's **AgentCert +
TrustScore** before it reaches an MCP server, wired as a standard Envoy
`ext_authz` check. Nothing here changes the gateway — it rides the existing
external-auth surface, runs alongside the Kuadrant `AuthPolicy` (Authorino),
and is opt-in per selector.

Follow-up to [#1520](https://github.com/Kuadrant/mcp-gateway/issues/1520).

## How it works

```
agent ──(x-agent-cert*)──▶ gateway ──ext_authz──▶ TAG sidecar ──▶ MCP server
                                                   (verify cert,
                                                    revocation,
                                                    TrustScore)
```

The verifier is an external sidecar — [`pdxlab/agentcert-tag`](https://github.com/pdxlab/agentcert-tag)
(public, MIT). It speaks the Envoy `ext_authz` HTTP check API on `/ext_authz`,
so Istio calls it as a `CUSTOM` external authorizer. The caller presents its
cert via `x-agent-cert*` request headers; the sidecar returns a verdict
(`VERIFIED` / `REVOKED` / `UNVERIFIED`) and a TrustScore, surfaced upstream as
`x-agentcert-*` response headers.

Mirrors the template recipe at
[`agentcert-tag/integrations/agentgateway`](https://github.com/pdxlab/agentcert-tag/tree/main/integrations/agentgateway).

## Shadow-mode by default

The sidecar runs with `TAG_MODE=shadow`: it decides and logs but **returns 200
on everything** — zero request impact. Watch `x-agentcert-shadow-would` (and
`x-agentcert-verdict` / `x-agentcert-trustscore`) to preview what enforcement
would do. To start enforcing, set `TAG_MODE=enforce` on the sidecar
(`VERIFIED` allows, everything else denies; optional `TAG_MIN_SCORE` floor).
No policy change is needed to flip modes.

## Files

| File | What |
|---|---|
| `sidecar.yaml` | The TAG verifier Deployment + Service (`TAG_MODE=shadow`). |
| `extension-provider.yaml` | Registers the sidecar as an Istio `envoyExtAuthzHttp` extension provider (mesh config). |
| `authorizationpolicy.yaml` | `AuthorizationPolicy` `action: CUSTOM` attaching the check to MCP traffic that carries a cert. |

## Apply

```bash
# 1. deploy the verifier (shadow mode)
kubectl apply -f sidecar.yaml

# 2. register it as an ext_authz provider — merge the extensionProviders entry
#    into the istio mesh config (do not blindly overwrite your mesh config):
#    kubectl -n istio-system get configmap istio -o yaml
kubectl apply -f extension-provider.yaml   # or merge by hand

# 3. attach the check (observes only, while in shadow)
kubectl apply -f authorizationpolicy.yaml
```

Then exercise the gateway and watch the sidecar:

```bash
kubectl logs -n mcp-system deploy/agentcert-tag
```

## Notes

- Decoupled: no code in the gateway, no change to its auth path. This is an
  Istio `CUSTOM` authorizer running next to the existing Authorino `AuthPolicy`.
- Opt-in: the `AuthorizationPolicy` `selector` + `when` clause scope it to
  cert-bearing MCP requests only; remove the manifests to turn it off.
- `failOpen: true` is set in the extension provider so an unreachable verifier
  can't block traffic while you're in shadow. Tighten it before you enforce.
- Header names (`x-agent-cert*` in, `x-agentcert-*` out) follow the sidecar's
  `/ext_authz` contract; all are configurable via `TAG_*` env vars on the
  sidecar. See `agentcert-tag/docs/ext_authz.md`.
