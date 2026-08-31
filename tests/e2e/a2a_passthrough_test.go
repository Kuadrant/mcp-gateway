//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A2A passthrough (phase 1): with --enable-a2a, the router lifts A2A protocol
// metadata off /a2a traffic into headers (x-a2a-agent from the path, x-a2a-method
// from the JSON-RPC envelope) for Telemetry and AuthPolicy, strips client-supplied
// copies, and fails closed on a POST it cannot label. It does not route — the
// user's own HTTPRoute carries the request to the agent. See
// docs/design/a2a/a2a-design.md (Implementation Phases) and
// docs/guides/a2a-passthrough.md.
//
// The suite is isolated on its own listener and MCPGatewayExtension so the
// --enable-a2a flag is toggled on a dedicated broker-router deployment, never the
// shared gateway. The flag is off by default; the disabled context asserts the
// A2A path stays inert until a user opts in.
const (
	a2aExtName   = "a2a-passthrough-ext"
	a2aNamespace = "mcp-a2a-passthrough"
	// the agent identity is the first path segment after /a2a/; it must equal the
	// AGENT_NAME the merged a2a test server advertises so the echoed request-info
	// can be matched against the router-set header.
	a2aTestAgent = "a2a-test-agent"
	a2aRoutePath = "/a2a/" + a2aTestAgent
	a2aRegPrefix = "a2areg_"
)

var _ = Describe("A2A Passthrough", Ordered, Label("A2A"), func() {
	var (
		a2aExt   *MCPGatewayExtensionSetup
		a2aRoute *gatewayapiv1.HTTPRoute
		// base URL for A2A requests on the a2a-passthrough listener. On Kind the
		// listener has its own port (gateway 8083 -> host 8013), like the other
		// isolated suites; on real clusters via the derived host.
		a2aBaseURL = func() string {
			if e2eDomain == defaultE2EDomain {
				return "http://" + a2aPassthroughServerHost("a2a") + ":8013"
			}
			return e2eScheme + "://" + a2aPassthroughServerHost("a2a")
		}()
		httpClient = func() *http.Client {
			c := e2eHTTPClient(a2aBaseURL)
			if c == nil {
				c = &http.Client{}
			}
			c.Timeout = 30 * time.Second
			return c
		}()
	)

	// a2aDo sends a raw HTTP request to the gateway and returns the status and body.
	a2aDo := func(method, path string, body []byte, hdrs map[string]string) (int, []byte) {
		var rc io.Reader
		if body != nil {
			rc = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, a2aBaseURL+path, rc)
		Expect(err).NotTo(HaveOccurred())
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		resp, err := httpClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		return resp.StatusCode, b
	}

	// sendMessageBody builds a v1.0 SendMessage JSON-RPC request whose text avoids
	// the "slow"/"fail" triggers so the task completes immediately with the echo and
	// request-info artifacts.
	sendMessageBody := func(text string) []byte {
		return []byte(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":%q}]}}}`,
			text))
	}

	// echoedHeaders pulls the request-info artifact's header map out of a completed
	// SendMessage response, so the test can assert what the agent actually received.
	echoedHeaders := func(body []byte) map[string]string {
		var r struct {
			Result struct {
				Task struct {
					Artifacts []struct {
						Name  string `json:"name"`
						Parts []struct {
							Data map[string]any `json:"data"`
						} `json:"parts"`
					} `json:"artifacts"`
				} `json:"task"`
			} `json:"result"`
		}
		// soft parse: a non-A2A or empty response (e.g. with the flag off, where the
		// request falls through the MCP path) yields no headers rather than a failure.
		if err := json.Unmarshal(body, &r); err != nil {
			return nil
		}
		for _, a := range r.Result.Task.Artifacts {
			if a.Name != "request-info" || len(a.Parts) == 0 {
				continue
			}
			raw, ok := a.Parts[0].Data["headers"].(map[string]any)
			if !ok {
				continue
			}
			out := map[string]string{}
			for k, v := range raw {
				if s, ok := v.(string); ok {
					out[strings.ToLower(k)] = s
				}
			}
			return out
		}
		return nil
	}

	// rpcErrorCode returns the JSON-RPC error code from a fail-closed response.
	rpcErrorCode := func(body []byte) (int, bool) {
		var r struct {
			Error *struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &r); err != nil || r.Error == nil {
			return 0, false
		}
		return r.Error.Code, true
	}

	deploymentHasFlag := func(g Gomega, flag string) {
		dep := &appsv1.Deployment{}
		g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: GatewayName, Namespace: a2aNamespace}, dep)).To(Succeed())
		g.Expect(dep.Spec.Template.Spec.Containers).NotTo(BeEmpty())
		g.Expect(dep.Spec.Template.Spec.Containers[0].Command).To(ContainElement(flag),
			"operator should preserve the user-added flag")
	}

	BeforeAll(func() {
		By("Creating the a2a-passthrough namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: a2aNamespace, Labels: map[string]string{"e2e": "test"}},
		}
		_ = k8sClient.Delete(ctx, ns)
		Eventually(func(g Gomega) {
			g.Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, ns))).NotTo(HaveOccurred())
		}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

		By("Creating a dedicated MCPGatewayExtension on the a2a-passthrough listener")
		a2aExt = NewMCPGatewayExtensionSetup(k8sClient).
			WithName(a2aExtName).
			InNamespace(a2aNamespace).
			TargetingGateway(GatewayName, GatewayNamespace).
			WithSectionName(A2APassthroughListenerName).
			WithPublicHost(A2APassthroughPublicHost).
			Build()
		a2aExt.Clean(ctx).Register(ctx)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPGatewayExtensionReady(ctx, k8sClient, a2aExtName, a2aNamespace)).To(Succeed())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Waiting for the dedicated broker-router deployment")
		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(ctx, a2aNamespace, GatewayName)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Registering an MCP server for the regression check")
		reg := NewTestResources("a2a-mcp-reg", k8sClient).
			InNamespace(a2aNamespace).
			WithBackendTarget("mcp-test-server1", 9090).
			WithBackendNamespace(TestServerNameSpace).
			WithHostname(a2aPassthroughServerHost("server")).
			WithPrefix(a2aRegPrefix).
			WithSectionName(A2APassthroughListenerName).
			WithParentGateway(GatewayName, GatewayNamespace).
			Build()
		server := reg.Register(ctx)
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Authoring the /a2a/{agent} HTTPRoute with a path rewrite to the agent's /a2a")
		// the router only lifts headers; the user's route carries the request to the
		// agent. The a2a test server serves POST /a2a, so the route rewrites
		// /a2a/{agent} -> /a2a. The route lives with the backend (mcp-test) to avoid a
		// ReferenceGrant, and attaches cross-namespace to the shared gateway.
		a2aRoute = &gatewayapiv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: "a2a-passthrough-route", Namespace: TestServerNameSpace},
			Spec: gatewayapiv1.HTTPRouteSpec{
				CommonRouteSpec: gatewayapiv1.CommonRouteSpec{
					ParentRefs: []gatewayapiv1.ParentReference{{
						Group:       ptr.To(gatewayapiv1.Group("gateway.networking.k8s.io")),
						Kind:        ptr.To(gatewayapiv1.Kind("Gateway")),
						Name:        gatewayapiv1.ObjectName(GatewayName),
						Namespace:   ptr.To(gatewayapiv1.Namespace(GatewayNamespace)),
						SectionName: ptr.To(gatewayapiv1.SectionName(A2APassthroughListenerName)),
					}},
				},
				Hostnames: []gatewayapiv1.Hostname{gatewayapiv1.Hostname(a2aPassthroughServerHost("a2a"))},
				Rules: []gatewayapiv1.HTTPRouteRule{{
					Matches: []gatewayapiv1.HTTPRouteMatch{{
						Path: &gatewayapiv1.HTTPPathMatch{
							Type:  ptr.To(gatewayapiv1.PathMatchPathPrefix),
							Value: ptr.To(a2aRoutePath),
						},
					}},
					Filters: []gatewayapiv1.HTTPRouteFilter{{
						Type: gatewayapiv1.HTTPRouteFilterURLRewrite,
						URLRewrite: &gatewayapiv1.HTTPURLRewriteFilter{
							Path: &gatewayapiv1.HTTPPathModifier{
								Type:               gatewayapiv1.PrefixMatchHTTPPathModifier,
								ReplacePrefixMatch: ptr.To("/a2a"),
							},
						},
					}},
					BackendRefs: []gatewayapiv1.HTTPBackendRef{{
						BackendRef: gatewayapiv1.BackendRef{
							BackendObjectReference: gatewayapiv1.BackendObjectReference{
								Name: gatewayapiv1.ObjectName("a2a-test-server"),
								Port: ptr.To(gatewayapiv1.PortNumber(9090)),
							},
						},
					}},
				}},
			},
		}
		_ = k8sClient.Delete(ctx, a2aRoute)
		Eventually(func(g Gomega) {
			g.Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, a2aRoute))).NotTo(HaveOccurred())
		}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

		By("Waiting for the route to be accepted by the gateway")
		// a hand-authored route gets Istio's Accepted/ResolvedRefs conditions; the
		// Programmed condition is only added by the MCP controller to routes it
		// manages, so check Accepted here.
		Eventually(func(g Gomega) {
			got := &gatewayapiv1.HTTPRoute{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: a2aRoute.Name, Namespace: a2aRoute.Namespace}, got)).To(Succeed())
			accepted := false
			for _, p := range got.Status.Parents {
				for _, c := range p.Conditions {
					if c.Type == "Accepted" && c.Status == metav1.ConditionTrue {
						accepted = true
					}
				}
			}
			g.Expect(accepted).To(BeTrue(), "route should be Accepted by the gateway")
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			DumpClusterState(ctx, a2aNamespace, GatewayNamespace)
		}
	})

	AfterAll(func() {
		// leave resources for post-failure debugging; BeforeAll Clean() handles
		// cleanup on the next run, and the namespace delete removes the route.
		_ = k8sClient.Delete(ctx, a2aRoute)
	})

	Context("with --enable-a2a disabled (default)", func() {
		It("[A2A,Negative] leaves the A2A path inert until opted in", func() {
			// with the flag off the A2A branch never runs, so the router neither strips
			// nor sets the A2A headers. Plant a client value and assert the router did
			// not replace it with its own path-derived value — proving the feature is
			// genuinely opt-in. (An unparseable-body -32700 check is not usable here: the
			// MCP path also rejects malformed JSON with -32700, so it cannot distinguish
			// the two.)
			planted := map[string]string{"x-a2a-agent": "client-planted"}
			Eventually(func(g Gomega) {
				_, body := a2aDo(http.MethodPost, a2aRoutePath, sendMessageBody("hello"), planted)
				hdrs := echoedHeaders(body)
				g.Expect(hdrs).NotTo(HaveKeyWithValue("x-a2a-agent", a2aTestAgent),
					"router must not set x-a2a-agent while --enable-a2a is off; body: %s", string(body))
				g.Expect(hdrs).NotTo(HaveKey("x-a2a-method"),
					"router must not set x-a2a-method while --enable-a2a is off; body: %s", string(body))
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		})
	})

	Context("with --enable-a2a enabled", Ordered, func() {
		BeforeAll(func() {
			By("Adding --enable-a2a to the dedicated broker-router")
			Expect(AddDeploymentCommandFlag(ctx, a2aNamespace, GatewayName, "--enable-a2a")).To(Succeed())

			By("Waiting for the flag to land and the deployment to roll out")
			Eventually(func(g Gomega) {
				deploymentHasFlag(g, "--enable-a2a")
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(WaitForDeploymentReady(ctx, a2aNamespace, GatewayName)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
		})

		AfterAll(func() {
			_ = RemoveDeploymentCommandFlag(ctx, a2aNamespace, GatewayName, "--enable-a2a")
		})

		It("[A2A] sets x-a2a-agent and x-a2a-method and strips client-supplied copies", func() {
			// spoof both router-owned headers; the router must strip them and set its
			// own, so the agent sees the path-derived agent and the parsed method, never
			// the client's planted values.
			spoof := map[string]string{
				"x-a2a-agent":  "spoofed",
				"x-a2a-method": "spoofed",
			}
			Eventually(func(g Gomega) {
				status, body := a2aDo(http.MethodPost, a2aRoutePath, sendMessageBody("hello"), spoof)
				g.Expect(status).To(Equal(http.StatusOK), "body: %s", string(body))
				hdrs := echoedHeaders(body)
				g.Expect(hdrs).NotTo(BeNil(), "request-info artifact should carry received headers; body: %s", string(body))
				g.Expect(hdrs).To(HaveKeyWithValue("x-a2a-agent", a2aTestAgent))
				g.Expect(hdrs).To(HaveKeyWithValue("x-a2a-method", "SendMessage"))
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		})

		It("[A2A] passes an unknown method through rather than failing it closed", func() {
			// an unknown but well-formed method is labelled "other" (bounded) and passed
			// through; the gateway must not fail it closed. The agent answers -32601
			// (method not found) — distinct from the gateway's -32700/-32600 fail-closed
			// — which proves the request reached the agent. The "other" normalization
			// itself is asserted in the router unit tests, since an unknown method yields
			// no completed task to echo the header back.
			body := []byte(`{"jsonrpc":"2.0","id":1,"method":"DropTables","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"x"}]}}}`)
			Eventually(func(g Gomega) {
				status, resp := a2aDo(http.MethodPost, a2aRoutePath, body, nil)
				g.Expect(status).To(Equal(http.StatusOK), "body: %s", string(resp))
				// the agent answers -32601 (method not found) — distinct from the
				// gateway's -32700/-32600 fail-closed — which proves the request was
				// labelled and forwarded to the agent rather than rejected.
				code, ok := rpcErrorCode(resp)
				g.Expect(ok).To(BeTrue(), "expected the agent's JSON-RPC error; body: %s", string(resp))
				g.Expect(code).To(Equal(-32601),
					"unknown method must reach the agent; body: %s", string(resp))
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		})

		It("[A2A,Negative] fails closed on an unparseable POST with -32700", func() {
			Eventually(func(g Gomega) {
				status, body := a2aDo(http.MethodPost, a2aRoutePath, []byte("{not json"), nil)
				g.Expect(status).To(Equal(http.StatusOK), "body: %s", string(body))
				code, ok := rpcErrorCode(body)
				g.Expect(ok).To(BeTrue(), "expected a JSON-RPC error; body: %s", string(body))
				g.Expect(code).To(Equal(-32700))
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		})

		It("[A2A,Negative] fails closed on a valid JSON body with no method with -32600", func() {
			Eventually(func(g Gomega) {
				status, body := a2aDo(http.MethodPost, a2aRoutePath, []byte(`{}`), nil)
				g.Expect(status).To(Equal(http.StatusOK), "body: %s", string(body))
				code, ok := rpcErrorCode(body)
				g.Expect(ok).To(BeTrue(), "expected a JSON-RPC error; body: %s", string(body))
				g.Expect(code).To(Equal(-32600))
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		})

		It("[A2A] passes a GET through to the agent (no fail-closed)", func() {
			// GET carries no body to label, so the router passes it through; the agent's
			// /a2a handler only accepts POST, so a 405 from the backend proves the GET
			// traversed the router and reached the agent rather than being rejected.
			Eventually(func(g Gomega) {
				status, _ := a2aDo(http.MethodGet, a2aRoutePath, nil, nil)
				g.Expect(status).To(Equal(http.StatusMethodNotAllowed))
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		})

		It("[A2A] does not disturb MCP traffic on the same gateway", func() {
			var c *mcp.ClientSession
			Eventually(func(g Gomega) {
				var err error
				c, err = NewStatefulClient(ctx, A2APassthroughGatewayURL)
				g.Expect(err).NotTo(HaveOccurred())
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
			defer func() { _ = c.Close() }()

			Eventually(func(g Gomega) {
				result, err := c.ListTools(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				var found bool
				for _, t := range result.Tools {
					if strings.HasPrefix(t.Name, a2aRegPrefix) {
						found = true
						break
					}
				}
				g.Expect(found).To(BeTrue(), "MCP tools/list should still return the registered server's tools with --enable-a2a on")
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
		})
	})
})
