# Aggregated config Secret size assessment

Investigation for [#1587](https://github.com/Kuadrant/mcp-gateway/issues/1587),
using upstream commit `26c7bb4ffa25ed31e3f166373a718f00ea0b50ff`.

## Conclusion

Further work is warranted for next-release planning. Sharing the gateway CA
bundle removes one source of duplication, but credentials, per-server CA bundles,
and virtual-server capability lists can still exceed the Secret limit. There is
no single safe server-count limit: the size of each entry matters.

Add byte-based reporting and a clear oversized-config error first. For larger
deployments, prototype bounded Secret-backed config fragments, referenced by a
small root config and mounted through a projected volume. Keep standalone mode's
single local YAML file supported. Do not require an external secret provider to
solve the generated-config size problem.

This is an assessment, not an implementation design or a claim about supported
gateway capacity. No runtime behavior, CRDs, deployment defaults, or reload
semantics are changed here. Implementation tasks and end-to-end tests should
follow agreement on an approach.

## Current data path and failure behavior

- [SecretReaderWriter](../../../internal/config/config_writer.go) serializes
  `BrokerConfig` with `sigs.k8s.io/yaml.Marshal`, writes `config.yaml` through
  `Secret.StringData`, and retries resource-version conflicts. Splitting that
  file into several keys in the **same Secret** would not avoid the size limit.
- The [registration controller](../../../internal/controller/mcpserverregistration_controller.go)
  copies resolved credentials, OAuth client credentials, and per-server CA PEM
  into server entries. Per-server CA input is capped at 64 KiB. The
  [gateway CA bundle](../../../internal/controller/ca_cert_bundle.go) is stored
  once and capped at 256 KiB. These input limits do not bound the aggregate.
- The [virtual-server controller](../../../internal/controller/mcpvirtualserver_controller.go)
  lists virtual servers across namespaces and writes their names, tool names,
  and prompt names to each gateway-extension namespace's config. Discovered
  upstream tool schemas are not stored in this `BrokerConfig`.
- The [broker/router deployment](../../../internal/controller/broker_router.go)
  mounts the generated Secret at `/config` without `subPath`. The combined
  [broker/router process](../../../cmd/mcp-broker-router/main.go) reads one YAML
  file and uses Viper to watch it. There is no config-fragment loader today.
- An oversized Secret update is rejected; conflict retry cannot fix its size.
  The stored Secret retains its last accepted contents. Registration reconciliation
  reports a failed write through its status/error path; virtual-server
  reconciliation returns an error. Broker replicas continue using their old
  config. A newly created Secret can still contain only the initial empty config
  if its subsequent populated update fails.
- Initial config-load failure stops startup. A reload failure logs an error and
  retains the previous runtime config. Any future fragment loader must preserve
  that snapshot behavior and make persistent staleness observable.

Kubernetes limits total Secret data to 1,048,576 bytes. Its
[validation code](https://github.com/kubernetes/kubernetes/blob/v1.37.1/pkg/apis/core/validation/validation.go#L7426-L7438)
sums the decoded values of all `data` keys and rejects totals above that limit.
`stringData` is converted into `data`; base64 expansion in a serialized Secret
does not reduce the usable data limit to 768 KiB. Metadata and transport/storage
overhead are outside this particular check.

## Measurement method

Run from the repository root, using the Go version and dependencies in `go.mod`:

<!-- cspell:ignore GOMAXPROCS GOFLAGS GOMEMLIMIT -->

```bash
GOMAXPROCS=2 GOFLAGS=-p=2 GOMEMLIMIT=256MiB \
  go run ./docs/design/config-secret-size/measure
go test ./docs/design/config-secret-size/measure
```

[The measurement program](measure/main.go) constructs the actual `BrokerConfig`,
`MCPServer`, and `VirtualServerConfig` types and uses the same YAML serializer as
the controller. [Recorded output](results.txt) includes exact byte counts for 10,
100, 500, and 1,000 servers, plus the first entry count above the limit for each
profile. The boundary search measures both adjacent counts.

These are synthetic sizing cases, not production samples. Each server has a
`team/server-00000`-style name, service URL, hostname, valid `server_00000`-style
prefix, `Enabled` state, and the default `uncategorised` category. The config also
contains the default `maxBodyBytes`. The credential is an opaque 1,024-byte string;
no real credentials are read. The deterministic synthetic CA certificate is
485 bytes; eight copies make a 3,880-byte bundle. Repeated certificates model the
serialized size of bundles, not a recommended trust configuration. CA-bearing
server entries use HTTPS URLs.

Profiles:

- `lean`: routing fields only; no credentials or CA data.
- `credential`: lean plus the 1 KiB credential on every server.
- `shared-ca`: credential plus one 3,880-byte gateway CA bundle.
- `per-server-ca`: credential plus a 3,880-byte CA bundle on every server.
- `large-server-ca`: credential plus a 65,475-byte CA bundle on every server,
  just below the controller's 64 KiB per-server input limit.
- `large-shared-ca`: credential plus one 261,900-byte gateway bundle,
  just below its 256 KiB input limit.
- `virtuals`: credential plus one virtual server per registered server, each
  selecting 100 tool names and 10 prompt names from its server's prefix.

Optional hints, tags, protocol overrides, OAuth settings, and guardrails are
omitted. Longer names, larger credentials, or those additional fields consume
more space. The runs serialize resolved config directly: they do not reconcile
CRDs, contact upstream MCP servers, submit Secrets to an API server, or benchmark
reload latency. Counts below the size limit therefore do not establish that the
rest of the gateway can support that workload.

## Results

All sizes below are decoded `config.yaml` bytes, including YAML indentation.
No other Secret data keys are assumed.

| Profile | 10 servers | 100 servers | 500 servers | 1,000 servers | First server count over 1 MiB |
|---|---:|---:|---:|---:|---:|
| lean | 1,961 | 19,331 | 96,531 | 193,031 | 5,433 |
| credential | 12,351 | 123,231 | 616,031 | 1,232,031 | 852 |
| shared-ca | 16,395 | 127,275 | 620,075 | 1,236,075 | 848 |
| per-server-ca | 54,161 | 541,331 | 2,706,531 | 5,413,031 | 194 |
| large-server-ca | 715,831 | 7,158,031 | 35,790,031 | 71,580,031 | 15 |
| large-shared-ca | 283,991 | 394,871 | 887,671 | 1,503,671 | 631 |
| virtuals | 41,637 | 415,947 | 2,079,547 | 4,159,047 | 253 |

The shared CA costs 4,044 serialized bytes once, rather than on every server.
The largest shared bundle costs 271,640 bytes once, including YAML formatting.
This confirms the benefit of shared trust while showing why it does not solve
growth in other fields.

Holding registered servers fixed at 100 isolates virtual-server growth:

| Virtual servers | Config bytes | Percent of limit |
|---:|---:|---:|
| 0 | 123,231 | 11.75% |
| 10 | 152,517 | 14.55% |
| 100 | 415,947 | 39.67% |
| 500 | 1,586,747 | 151.32% |
| 1,000 | 3,050,247 | 290.89% |

For this fixture, each additional virtual server after the first adds 2,927
bytes. The first count above the limit is 317 virtual servers. This limit can be
reached without adding any registered servers.

These are conditional boundaries, not product limits. Even a small server count
can exhaust the Secret with large resolved credentials; there is no explicit
credential-length cap in `buildMCPServerConfig`. Existing CA input caps alone
cannot provide aggregate safety.

## Storage options

The file format and the delivery mechanism are separate choices: root references
can be combined with projected volumes. None of the options removes the need to
bound the size of each generated object.

| Approach | Reloads and errors | Credential boundaries | Operational cost |
|---|---|---|---|
| Root config referencing files backed by separate Secrets | Needs a new loader that watches the root and fragments, validates the entire set, and swaps one complete snapshot. Missing or mismatched fragments must fail startup or preserve the previous snapshot on reload. | Keep secret values in Secrets; accept only controller-generated paths under the config mount. Do not expose resolved credentials in router/client output or logs. | Native Kubernetes objects, but more objects and controller coordination. Mounting each Secret separately or changing the mounted set requires a Pod-template update. |
| Project several Secrets into one directory | Supplies files, not YAML merging. The loader still needs the rules above. Separate Secret updates are not a transaction, so a projected directory must not be assumed to contain one logical config generation. | Sources must be in the Pod's namespace; the controller must create scoped copies there. Project only the required keys. | Avoids a separate mount per fragment. The source list is still in the Pod spec: adding a source requires a rollout. Fixed shards can reduce rollouts but need a sizing/growth policy. |
| Projected inputs assembled into a file in `emptyDir` | An init container only assembles at startup. Live updates need a sidecar that watches every input, validates the set, and atomically renames the result. Never append or concatenate YAML blindly. | A second process handles all resolved credentials. Use a memory-backed volume and restrictive permissions if this route is chosen; do not log contents. | Keeps the application's one-file loader, but adds another image, memory copy, update protocol, health checks, and failure modes. Disk-backed `emptyDir` is unsuitable for copying these secrets. |
| External provider, such as Secrets Store CSI Driver | Provider-mounted files still need application reload handling and a coherent config snapshot. Rotation must be configured and tested. Syncing the whole aggregate back into one Kubernetes Secret retains the same size problem. | Requires provider identity, authorization, and rotation policy. It does not automatically resolve the controller's existing Secret references or assemble gateway config. | Adds the driver, provider, CRDs, credentials, and an external availability dependency. Appropriate when a deployment already requires external secret management; disproportionate as the default answer to generated-config growth. |

Kubernetes documents [projected-volume namespace restrictions](https://kubernetes.io/docs/concepts/storage/projected-volumes/)
and [eventual Secret updates and the `subPath` limitation](https://kubernetes.io/docs/concepts/configuration/secret/#using-secrets-as-files-from-a-pod).
The CSI driver documents its [provider model](https://secrets-store-csi-driver.sigs.k8s.io/concepts.html)
and [rotation setup and application responsibilities](https://secrets-store-csi-driver.sigs.k8s.io/topics/secret-auto-rotation).

## Recommendation for next-release planning

1. **Report actual bytes before changing storage.** Measure the final serialized
   Secret data on every write path, including gateway and virtual-server writes.
   Propose a warning at 80% of the limit and a size-specific error above the hard
   limit, with byte count and entry counts rather than secret contents. Expose
   enough status/metrics to detect an old config still being served. The warning
   threshold is a proposed headroom policy, not a measured safety guarantee.
2. **Prototype root references with bounded Secret fragments and projection.**
   Batch by serialized bytes instead of allocating one Secret per server. A
   starting prototype target of 512 KiB per fragment leaves headroom; measure the
   root's size too. Keep the projected source set bounded and document that growth
   requiring another source rolls Pods. Validate namespace scoping, object quota,
   and API/kubelet load before selecting a supported shard count.
3. **Define publication and reload consistency before implementation.** Coordinate
   the current registration, gateway, and virtual-server writers so they cannot
   publish conflicting generations. A root should identify the expected fragment
   set and content hashes. Write fragments before publishing the root; readers
   must reject mixed or incomplete sets, retry after either root or fragment
   changes, and apply only a fully validated snapshot. File projection is
   eventually consistent even if API writes were ordered. Bound retention of old
   fragments and clean them up safely. Decide how persistent failure affects
   readiness and credential revocation rather than serving stale data silently.
4. **Keep standalone operation independent.** A local single-file config continues
   through the existing path, with no Kubernetes API client or CSI requirement.
   Any future include format should be optional and locally resolvable.

Before implementing, validate API-server rejection just below/above the byte
boundary and exercise fragment loss, invalid YAML, mismatched hashes, concurrent
writers, certificate/credential rotation, deletion, Pod restarts, and rollbacks.
Check broker-only credential use, combined-process file access, and that errors
never print secret values. Benchmark reloads and controller/API cost with the
same large profiles. These are follow-up validation requirements; this
investigation has not run a cluster or implemented fragment storage.
