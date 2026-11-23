package controller

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/protocol"
)

func TestMergeVersions(t *testing.T) {
	observed := map[string]struct{}{"2026-07-28": {}, "not-a-date": {}, "": {}}
	require.Equal(t, []string{"2025-11-25", "2026-07-28"}, mergeVersions([]string{"2025-11-25"}, observed))

	many := map[string]struct{}{}
	for d := 1; d <= 12; d++ {
		many[time.Date(2025, 1, d, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)] = struct{}{}
	}
	got := mergeVersions(nil, many)
	require.Len(t, got, maxUsedVersions)
	require.Equal(t, "2025-01-12", got[len(got)-1], "keeps the newest")
}

func brokerPodObj(ns, name, ip string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(ns + "/" + name), Labels: brokerRouterLabels()},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ip},
	}
}

func TestCollect(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, mcpv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	extA := &mcpv1.MCPGatewayExtension{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "team-a"}}
	extB := &mcpv1.MCPGatewayExtension{ObjectMeta: metav1.ObjectMeta{Name: "gw", Namespace: "team-b"}}
	shared := &mcpv1.MCPServerRegistration{ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: "servers"}}
	quiet := &mcpv1.MCPServerRegistration{ObjectMeta: metav1.ObjectMeta{Name: "quiet", Namespace: "servers"}}
	statusWrites := map[string]int{}
	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(shared, quiet).
		WithObjects(extA, extB, shared, quiet,
			brokerPodObj("team-a", "broker-1", "10.0.0.1"),
			brokerPodObj("team-a", "broker-2", "10.0.0.2"),
			brokerPodObj("team-b", "broker-1", "10.0.0.3")).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				statusWrites[obj.GetName()]++
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
		}).
		Build()

	v26, v25 := protocol.Version2026, protocol.Version2025
	responses := map[string]*protocol.Usage{
		// two gateways and two replicas serving the same upstream
		"10.0.0.1:9090": {Servers: map[string][]string{"servers/shared": {v25}}},
		"10.0.0.2:9090": {Servers: map[string][]string{"servers/shared": {v25}, "servers/deleted": {v26}}},
		"10.0.0.3:9090": {Servers: map[string][]string{"servers/shared": {v25}}},
	}
	c := &ProtocolUsageCollector{
		Client: k8s,
		Reader: k8s,
		Logger: slog.New(slog.DiscardHandler),
		fetch: func(_ context.Context, addr string) (*protocol.Usage, error) {
			if u, ok := responses[addr]; ok {
				return u, nil
			}
			return nil, errors.New("unreachable")
		},
	}
	c.init()
	used := func(reg *mcpv1.MCPServerRegistration) []string {
		got := &mcpv1.MCPServerRegistration{}
		require.NoError(t, k8s.Get(t.Context(), client.ObjectKeyFromObject(reg), got))
		return got.Status.UsedProtocolVersions
	}

	c.collect(t.Context())
	require.Equal(t, []string{v25}, used(shared))
	require.Empty(t, used(quiet))

	// nothing new: no write
	c.collect(t.Context())
	require.Equal(t, 1, statusWrites["shared"])

	// a new version on any broker adds it once
	responses["10.0.0.3:9090"].Servers["servers/shared"] = []string{v25, v26}
	c.collect(t.Context())
	c.collect(t.Context())
	require.Equal(t, []string{v25, v26}, used(shared))
	require.Equal(t, 2, statusWrites["shared"])

	// versions are kept after every broker restarts and forgets them
	for _, u := range responses {
		u.Servers = map[string][]string{}
	}
	c.collect(t.Context())
	require.Equal(t, []string{v25, v26}, used(shared))
	require.Equal(t, 2, statusWrites["shared"])
	require.Zero(t, statusWrites["quiet"])
}

func TestFetchUsage(t *testing.T) {
	c := &ProtocolUsageCollector{}
	c.init()

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, protocol.UsagePath, r.URL.Path)
		_, _ = w.Write([]byte(`{"servers":{"ns/a":["2026-07-28"]}}`))
	}))
	defer ok.Close()
	usage, err := c.fetchUsage(t.Context(), strings.TrimPrefix(ok.URL, "http://"))
	require.NoError(t, err)
	require.Equal(t, map[string][]string{"ns/a": {"2026-07-28"}}, usage.Servers)

	// a broker from before this feature has no endpoint: an error, so nothing is written
	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	_, err = c.fetchUsage(t.Context(), strings.TrimPrefix(old.URL, "http://"))
	require.ErrorContains(t, err, "unexpected status 404")
}
