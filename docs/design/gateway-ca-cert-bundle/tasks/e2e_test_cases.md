## Gateway CA Certificate Bundle E2E Test Cases

---
test_suite: ca_bundle_test.go
tags: Happy,CACertBundle
---

### [Happy,CACertBundle] Gateway CA bundle enables TLS connection to upstream server

- When an MCPGatewayExtension is configured with `caCertBundleRef` pointing to a Secret containing the CA that signed an upstream TLS server's certificate, and an MCPServerRegistration for that server does NOT have `caCertSecretRef`, the broker should use the gateway-level CA bundle to verify the TLS connection. The upstream server's tools should be discovered and callable through the gateway.

### [Happy,CACertBundle] Multiple servers sharing the same gateway CA bundle

- When an MCPGatewayExtension has `caCertBundleRef` set and two MCPServerRegistrations target upstream TLS servers whose certificates are signed by the same CA (referenced by the bundle), both servers should have their tools discovered and callable without either registration needing `caCertSecretRef`. This verifies the shared trust pool works across multiple servers.

### [Happy,CACertBundle] Per-server CA appends to gateway bundle

- When an MCPGatewayExtension has `caCertBundleRef` set with CA-A, and an MCPServerRegistration has `caCertSecretRef` set with CA-B (a different CA), the broker should trust both CA-A and CA-B for that server. A server whose certificate is signed by CA-B should connect successfully. This verifies additive behavior.

### [CACertBundle] Gateway bundle alone insufficient for server with unique CA

- When an MCPGatewayExtension has `caCertBundleRef` set with CA-A, and an upstream server's certificate is signed by CA-B (not in the gateway bundle), and the MCPServerRegistration does NOT have `caCertSecretRef`, the broker should fail the TLS handshake with a certificate verification error. The MCPServerRegistration should not become Ready.

### [CACertBundle] No gateway bundle, no per-server CA — public CA works

- When neither `caCertBundleRef` nor `caCertSecretRef` is set, servers using publicly-trusted CAs should continue to work. Servers using private CAs should fail with certificate verification errors. This verifies no regression in default TLS behavior.

### [CACertBundle] Invalid CA bundle secret — MCPGatewayExtension reports error

- When `caCertBundleRef` references a Secret that does not exist, the MCPGatewayExtension should report a status condition with an error message mentioning the missing Secret. Similarly, a Secret without the required label `mcp.kuadrant.io/secret=true` should result in a validation error in the status.

### [CACertBundle] CA bundle rotation updates broker trust pool

- When the CA bundle Secret is updated with a new CA certificate (e.g. after CA rotation), the controller should detect the change, re-validate the PEM, and write the updated `gatewayCACertPEM` into the config secret. The config change flows through `mcpConfig.Notify()`, triggering the broker to rebuild its trust pool. After the update propagates (~60-120s kubelet volume sync), servers whose certificates are signed by the new CA should connect successfully.

### [Happy,CACertBundle] Gateway CA bundle with existing per-server CA on same server

- When an MCPGatewayExtension has `caCertBundleRef` set with a CA that covers an upstream TLS server, and the same server's MCPServerRegistration also has `caCertSecretRef` pointing to the same CA, the broker should connect successfully. Both the gateway bundle and per-server CA contribute to the trust pool additively. This verifies no conflict when the same CA appears in both places.
