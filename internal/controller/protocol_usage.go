package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/protocol"
)

// +kubebuilder:rbac:groups="",resources=pods,verbs=list

const (
	// cap on a broker usage response; it holds one short entry per upstream
	maxUsageBytes = 1 << 20
	// matches the status.usedProtocolVersions MaxItems
	maxUsedVersions = 8
)

var protocolVersionPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// ProtocolUsageCollector periodically reads from broker pods which protocol
// versions clients used with each upstream, and adds new ones to that
// MCPServerRegistration's status.usedProtocolVersions. Versions are only
// added, so each registration is written once per newly seen version.
type ProtocolUsageCollector struct {
	client.Client
	// Reader must be uncached: listing pods through the manager cache would
	// start a cluster-wide pod informer
	Reader   client.Reader
	Logger   *slog.Logger
	Interval time.Duration
	HTTP     *http.Client

	// fetch is overridable in tests
	fetch func(ctx context.Context, addr string) (*protocol.Usage, error)
}

var _ manager.Runnable = &ProtocolUsageCollector{}
var _ manager.LeaderElectionRunnable = &ProtocolUsageCollector{}

// NeedLeaderElection keeps a single collector writing status.
func (c *ProtocolUsageCollector) NeedLeaderElection() bool { return true }

// Start runs the collection loop until ctx is cancelled.
func (c *ProtocolUsageCollector) Start(ctx context.Context) error {
	c.init()
	c.Logger.Info("starting protocol usage collector", "interval", c.Interval)
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	for {
		c.collect(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (c *ProtocolUsageCollector) init() {
	if c.Interval <= 0 {
		c.Interval = time.Hour
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if c.fetch == nil {
		c.fetch = c.fetchUsage
	}
}

func (c *ProtocolUsageCollector) collect(ctx context.Context) {
	var exts mcpv1.MCPGatewayExtensionList
	if err := c.Reader.List(ctx, &exts); err != nil {
		c.Logger.Error("protocol usage: failed to list MCPGatewayExtensions", "error", err)
		return
	}
	namespaces := map[string]struct{}{}
	for i := range exts.Items {
		namespaces[exts.Items[i].Namespace] = struct{}{}
	}

	used := map[string]map[string]struct{}{}
	for _, ns := range slices.Sorted(maps.Keys(namespaces)) {
		addrs, err := c.brokerAddrs(ctx, ns)
		if err != nil {
			c.Logger.Error("protocol usage: failed to list broker pods", "namespace", ns, "error", err)
			continue
		}
		for _, addr := range addrs {
			usage, err := c.fetch(ctx, addr)
			if err != nil {
				c.Logger.Debug("protocol usage: failed to read broker", "namespace", ns, "addr", addr, "error", err)
				continue
			}
			for server, versions := range usage.Servers {
				set := used[server]
				if set == nil {
					set = map[string]struct{}{}
					used[server] = set
				}
				for _, v := range versions {
					set[v] = struct{}{}
				}
			}
		}
	}
	for _, server := range slices.Sorted(maps.Keys(used)) {
		if err := c.addUsedVersions(ctx, server, used[server]); err != nil {
			c.Logger.Error("protocol usage: failed to update used protocol versions", "server", server, "error", err)
		}
	}
}

// addUsedVersions adds versions not yet in the registration's status.
func (c *ProtocolUsageCollector) addUsedVersions(ctx context.Context, server string, observed map[string]struct{}) error {
	ns, name, ok := strings.Cut(server, "/")
	if !ok {
		return nil
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		reg := &mcpv1.MCPServerRegistration{}
		if err := c.Reader.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, reg); err != nil {
			return client.IgnoreNotFound(err)
		}
		merged := mergeVersions(reg.Status.UsedProtocolVersions, observed)
		if slices.Equal(merged, reg.Status.UsedProtocolVersions) {
			return nil
		}
		reg.Status.UsedProtocolVersions = merged
		return c.Status().Update(ctx, reg)
	})
}

// mergeVersions returns existing plus the date-shaped observed versions,
// sorted and capped to what the schema accepts. values are re-checked since
// the broker endpoint is unauthenticated.
func mergeVersions(existing []string, observed map[string]struct{}) []string {
	set := map[string]struct{}{}
	for _, v := range existing {
		set[v] = struct{}{}
	}
	for v := range observed {
		if protocolVersionPattern.MatchString(v) {
			set[v] = struct{}{}
		}
	}
	out := slices.Sorted(maps.Keys(set))
	if len(out) > maxUsedVersions {
		// keep the newest
		out = out[len(out)-maxUsedVersions:]
	}
	return out
}

func (c *ProtocolUsageCollector) brokerAddrs(ctx context.Context, ns string) ([]string, error) {
	var pods corev1.PodList
	if err := c.Reader.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels(brokerRouterLabels())); err != nil {
		return nil, err
	}
	var out []string
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase != corev1.PodRunning || p.Status.PodIP == "" {
			continue
		}
		out = append(out, net.JoinHostPort(p.Status.PodIP, strconv.Itoa(brokerMetricsPort)))
	}
	return out, nil
}

func (c *ProtocolUsageCollector) fetchUsage(ctx context.Context, addr string) (*protocol.Usage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+protocol.UsagePath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	usage := &protocol.Usage{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxUsageBytes)).Decode(usage); err != nil {
		return nil, fmt.Errorf("decode usage: %w", err)
	}
	return usage, nil
}
