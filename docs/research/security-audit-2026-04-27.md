# MCP Gateway Security Audit — 2026-04-27

## Consolidated Findings

| ID | Category | File:Line | Severity | Finding | Remediation | Evaluation |
|----|----------|-----------|----------|---------|-------------|------------|
| AUTH-1 | Session/Auth | `internal/mcp-router/request_handlers.go:520-528` | **Critical** | All client headers (including `authorization`, `cookie`) passed through to upstream backends during session init. External MCP server operators can harvest user OAuth tokens. **Mitigated by AuthPolicy**: AuthPolicy per-HTTPRoute validates the client token and can replace the authorization header with the backend-specific credential. Without AuthPolicy applied, this is exploitable. | Ensure AuthPolicy is always applied to upstream MCP server HTTPRoutes. Document that running without AuthPolicy exposes client credentials to backends. Consider building an explicit header allowlist as defense-in-depth. | MCP Gateway is a proxy — it has no context on auth. Auth is controlled by user-managed AuthPolicies that validate the auth header before requests reach the gateway. It is the user's responsibility to define AuthPolicies per HTTPRoute. |
| S-01 | Credentials | `cmd/mcp-broker-router/main.go:105` | **Critical** | Hardcoded default router API key `"secret-api-key"`. In standalone mode, if env var unset, gateway accepts hairpin init requests with a publicly known string. | Remove default. Panic at startup if neither env var nor flag is set. | |
| AUTH-4 | Session/Auth | `internal/broker/filtered_tools_handler.go:167-196` | **High** | `x-mcp-virtualserver` header accepted as plain string with no integrity protection. Any client can set it to any `namespace/name` and change visible tools — tenant boundary bypass. | Sign the header with ECDSA (same as `x-authorized-tools`) or strip from client requests at Envoy level. | Although this header can be passed by anyone, the broker applies tool filters to ensure the user only sees what they are allowed to. |
| AUTH-3 | Envoy/Istio | `internal/mcp-router/request_handlers.go:610-622` | **High** | `mcp-init-host` + `router-key` allows `:authority` rewrite. Router key is deterministic SHA-256 of UID, visible in pod command args. Attacker with pod-read RBAC can redirect init requests to arbitrary hosts. | Generate key from CSPRNG, store in Secret, sign hairpin requests with HMAC. | |
| S-02 | Credentials | `internal/controller/broker_router.go:78` | **High** | Router key exposed as CLI arg in deployment spec — visible via `kubectl describe pod` and audit logs. | Move to env var sourced from a Kubernetes Secret. | |
| S-03 | Credentials | `cmd/mcp-broker-router/main.go:169-176` | **High** | Redis connection accepts plaintext `redis://` with no TLS enforcement. Session data traverses the network unencrypted. | Reject `redis://` in production; require `rediss://` or add explicit `--redis-allow-plaintext` flag. | |
| S-04 | Credentials | `internal/session/jwt.go:42-56` | **High** | No JWT signing key rotation mechanism. Rotating the key invalidates all sessions simultaneously. | Implement key ring with `kid` header; sign with newest, verify against all. | |
| SSRF-1 | Go Security | `internal/broker/upstream/mcp.go:97` | **High** | Broker connects to URLs built from user-created CRDs. Cluster users with CRD write access can trigger SSRF to internal metadata endpoints (169.254.169.254, etc.). | Apply egress NetworkPolicy; restrict MCPServerRegistration creation via RBAC. | |
| RBAC-1 | RBAC | `config/rbac/role.yaml:16-25` | **High** | Cluster-wide CRUD on all secrets. Informer is label-filtered, but RBAC permits reading any secret in any namespace via direct API calls. | Split into namespace-scoped Roles for secrets or document as accepted trust boundary. | |
| SC-1 | Supply Chain | Makefile, workflows | **High** | No `govulncheck` in CI or Makefile. Known Go CVEs won't be caught before merge. | Add `govulncheck ./...` to CI and Makefile. | |
| SC-2 | Supply Chain | `.github/workflows/*.yaml` | **High** | No SBOM generation on any image. | Add `anchore/sbom-action` after image build. | |
| SC-3 | Supply Chain | `.github/workflows/images.yaml` | **High** | No container image signing (cosign/Notary). Published images cannot be verified. | Add `sigstore/cosign-installer` + `cosign sign` with GitHub OIDC keyless signing. | |
| PSS-01 | Pod Security | `Dockerfile:16-28` | **High** | No `USER` directive — container runs as root. | Add `RUN adduser -D -u 65532 nonroot` and `USER 65532:65532`. | |
| PSS-02 | Pod Security | `Dockerfile.controller:16-27` | **High** | Same: no `USER` directive, runs as root. | Same fix as PSS-01. | |
| PSS-03 | Pod Security | `charts/.../deployment-controller.yaml:19-61` | **High** | No `securityContext` at all. Fails Restricted PSS entirely. | Add `runAsNonRoot`, `readOnlyRootFilesystem`, `drop ALL`, `seccompProfile: RuntimeDefault`. | |
| AUTH-2 | Envoy/Istio | `mcpgatewayextension_controller.go:793-827` | **Medium** | EnvoyFilter has no owner reference (cross-namespace). Orphans possible if finalizer is bypassed. | Add periodic reconciliation to clean orphaned EnvoyFilters. | |
| PATH-1 | Go Security | `api/v1alpha1/types.go:53-58` | **Medium** | No CRD validation on `spec.path` field. Could inject path traversal or malformed URLs. | Add `+kubebuilder:validation:Pattern="^/[a-zA-Z0-9/_.-]*$"` and `MaxLength=256`. | |
| HDR-1 | Go Security | `internal/mcp-router/headers.go:52-159` | **Medium** | No sanitization of header values for `\r\n` or null bytes in `HeadersBuilder`. | Strip control characters from all header values before setting. | |
| RACE-1 | Go Security | `internal/mcp-router/server.go:50` | **Medium** | Race condition on `RoutingConfig` pointer — written by `OnConfigChange` goroutine, read by `Process` goroutines without sync. | Use `atomic.Pointer[config.MCPServersConfig]`. | |
| RACE-2 | Go Security | `internal/broker/broker.go:213-216` | **Medium** | `RegisteredMCPServers()` returns internal map reference outside lock scope. Concurrent mutation possible. | Return a shallow copy of the map. | |
| PSS-04 | Pod Security | `internal/controller/broker_router.go:120-185` | **Medium** | Controller-generated broker-router Deployment has no `securityContext`. | Add pod and container security contexts in `buildBrokerRouterDeployment()`. | |
| PSS-06 | Pod Security | `charts/mcp-gateway/` | **Medium** | No `NetworkPolicy` in chart. Broker-router has unrestricted ingress/egress. | Add NetworkPolicy restricting ingress to 8080/50051, egress to DNS + upstreams. | |
| PSS-07 | Pod Security | `cmd/main.go:85` | **Medium** | Metrics endpoint on `:8082` with no authentication. | Use `SecureServing: true` or bind to localhost. | |
| S-05 | Credentials | `internal/clients/clients.go:30` | **Medium** | Hairpin initialization uses plaintext HTTP, leaking router-key and client headers. | Use `https://` or document mesh-provided mTLS assumption. | |
| S-06 | Credentials | `cmd/mcp-broker-router/main.go:343` | **Medium** | gRPC ext_proc server has no TLS. | Add `grpc.Creds(credentials.NewTLS(...))` or document mesh reliance. | |
| S-07 | Credentials | `internal/broker/filtered_tools_handler.go:95-96` | **Medium** | Trusted headers public key loaded once at startup, no rotation support. | Load from Secret volume, watch for changes, add `kid` support. | |
| RBAC-2 | RBAC | `config/rbac/mcpgatewayextension_admin_role.yaml:18-20` | **Medium** | Admin role uses `verbs: ['*']` — includes `escalate`, `bind`, `deletecollection`. | Replace with explicit verb list. | |
| SC-4 | Supply Chain | `.github/workflows/images.yaml:178,230` | **Medium** | Bundle and catalog images have `provenance: false`. | Set `provenance: true` on all `docker/build-push-action`. | |
| SC-5 | Supply Chain | `.github/workflows/*.yaml` | **Medium** | All GitHub Actions pinned to mutable tags, not commit SHAs. | Pin to full SHAs; add `github-actions` to Dependabot. | |
| SC-6 | Supply Chain | `.github/dependabot.yml` | **Medium** | Dependabot only covers `gomod` — no coverage for Actions, Docker, npm, pip. | Add `github-actions` and `docker` ecosystems. | |
| SC-7 | Supply Chain | `Dockerfile:1, Dockerfile.controller:1` | **Medium** | Base images use mutable tags, not digest pins. | Pin to `@sha256:...` digests. | |
| SC-9 | CI/CD | 10 workflows | **Medium** | Missing `permissions:` blocks — inherit default (often `write-all`). | Add `permissions: contents: read` as top-level default. | |
| SC-10 | CI/CD | All workflows | **Medium** | `persist-credentials: false` never set on `actions/checkout`. | Add to all checkout steps. | |
| SC-11 | CI/CD | `.github/workflows/e2e-on-demand.yaml:36` | **Low** | Command injection via `${{ github.event.comment.body }}` in shell context. Org-membership check partially mitigates. | Use `env:` block instead of inline expression. | |
| S-08 | Credentials | `internal/config/types.go:65` | **Low** | Credential stored as plaintext string in config YAML, logged at DEBUG level. | Never log credential values; reference by Secret name/key. | |
| S-09 | Credentials | `internal/session/cache.go:103` | **Low** | Redis elicitation keys have no TTL — accumulate indefinitely. | Set TTL matching session duration. | |
| RBAC-3 | RBAC | `config/rbac/role.yaml:48-51` | **Low** | Unnecessary `gateways/status` update permission cluster-wide. | Scope to specific Gateway names via `resourceNames` if possible. | |
| RBAC-4 | RBAC | `config/rbac/mcpgatewayextension_editor_role.yaml:20-28` | **Low** | Editor role includes `patch` but operator role does not — minor inconsistency. | Remove `patch` if users don't need strategic merge patches. | |
| ERR-1 | Go Security | `internal/otel/logging.go:77` | **Low** | Discarded error from slog handler `_ = h.Handle(ctx, r.Clone())`. | Log error to stderr or fallback handler. | |
| ERR-2 | Go Security | `cmd/mcp-broker-router/main.go:51-53` | **Low** | Discarded `AddToScheme` errors during `init()`. | Use `utilruntime.Must()` for fail-fast. | |
| LEAK-1 | Go Security | `internal/broker/broker.go:198` | **Low** | Goroutine leak potential — `manager.Start(ctx)` launched with no join on shutdown. | Use `sync.WaitGroup` or `errgroup.Group`. | |

## Top 3 Most Impactful Findings

**1. S-01 (Critical) — Hardcoded default router API key.** The `"secret-api-key"` default means any standalone deployment that doesn't explicitly override the key is trivially exploitable. Combined with the router key being visible in pod command args (S-02) and the deterministic key derivation (AUTH-3), the hairpin initialization pathway has multiple weaknesses that should be addressed together.

**2. SC-1/2/3 (High) — No supply chain verification.** The absence of govulncheck, SBOM generation, and image signing means there is no mechanism to detect vulnerable dependencies before merge, no way for consumers to verify image contents, and no cryptographic proof of image provenance. For a security-critical infrastructure component like a gateway, these are table-stakes expectations.

**3. AUTH-4 (High) — `x-mcp-virtualserver` header with no integrity protection.** Unlike `x-authorized-tools` (signed JWT), this header is accepted as a plain string. Any client that can reach the gateway can impersonate any virtual server, bypassing tenant-level tool isolation.

## Recommended Priority Order

1. S-01 + S-02 + AUTH-3 — Fix router key defaults, exposure, and derivation
2. PSS-01/02/03 — Dockerfile USER + securityContext (trivial fixes, high impact)
3. SC-1/2/3 — govulncheck, SBOM, image signing
4. AUTH-4 — Virtual server header integrity
5. SSRF-1 + PSS-06 — NetworkPolicy covers both egress restriction and SSRF mitigation
6. Remaining Medium findings

## Not Flagged (Correctly Handled)

- JWT algorithm pinning: both session JWTs (HMAC) and trusted-header JWTs (ES256) enforce specific signing methods
- `-race` flag used in unit test and build targets
- Credential secrets require `mcp.kuadrant.io/secret=true` label
- Broker-router SA token auto-mount disabled
- Config volume mounted read-only
- Router-key and mcp-init-host headers stripped before forwarding to backends
- Session JWTs include proper claims (exp, nbf, iat, iss, aud, jti)
- Session IDs generated via `uuid.NewString()`
- `failure_mode_allow: false` on ext_proc EnvoyFilter
- Owner references set on all in-namespace resources (Deployment, Service, SA, HTTPRoute, Secret)
- AuthPolicy per-HTTPRoute governs authorization header passthrough to upstream MCP servers (not a credential leak)
