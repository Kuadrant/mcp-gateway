# Optional TOA verify with policy attachment

Kuadrant MCP Gateway aggregates MCP servers behind Envoy with authN/authZ and
rate-limit policy attachment. That answers gateway policy. It does not prove
tool delivery from an outside probe.

[TOA](https://github.com/Carmel-Labs-Inc/toa) (`toa/0.1`) is optional offline
delivery evidence before attaching a new `MCPServer` or promoting a route.

```yaml
      - name: Verify tool delivery attestation
        if: hashFiles('toa.json') != ''
        run: |
          pip install "git+https://github.com/Carmel-Labs-Inc/toa.git@345f24607919b5bdf143719b9ea062543cdfe88e#subdirectory=python"
          toa-verify toa.json --require-layer functional=pass
```

Example: [`../../examples/toa-after-policy.yml`](../../examples/toa-after-policy.yml).

Not per-call. No AgentStatus account required to verify.
