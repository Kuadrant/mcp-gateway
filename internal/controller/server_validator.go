package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/broker"
	discoveryv1 "k8s.io/api/discovery/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ServerValidator validates MCP servers by calling broker endpoints
type ServerValidator struct {
	k8sClient  client.Client
	httpClient *http.Client
	namespace  string
}

// NewServerValidator creates a new server validator
func NewServerValidator(k8sClient client.Client) *ServerValidator {
	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		namespace = "mcp-system"
	}

	return &ServerValidator{
		k8sClient: k8sClient,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		namespace: namespace,
	}
}

// ValidateServers validates MCP servers by calling the broker's /status endpoints
func (v *ServerValidator) ValidateServers(ctx context.Context, namespace string) (*broker.StatusResponse, error) {
	logger := log.FromContext(ctx)
	// get endpoint slices for the broker service
	endpointSliceList := &discoveryv1.EndpointSliceList{}
	err := v.k8sClient.List(ctx, endpointSliceList, client.InNamespace(namespace), client.MatchingLabels{
		"app.kubernetes.io/name": "mcp-gateway",
	})
	if err != nil {
		logger.Error(err, "Failed to get endpoint slices for mcp-broker service")
		return nil, fmt.Errorf("failed to get endpoint slices: %w", err)
	}

	// collect all endpoint addresses
	var addresses []string
	for _, endpointSlice := range endpointSliceList.Items {
		for _, endpoint := range endpointSlice.Endpoints {
			if endpoint.Conditions.Ready != nil && *endpoint.Conditions.Ready {
				for _, addr := range endpoint.Addresses {
					// use the status port
					url := fmt.Sprintf("http://%s/status", net.JoinHostPort(addr, "8080"))
					addresses = append(addresses, url)
				}
			}
		}
	}

	if len(addresses) == 0 {
		logger.Info("No broker endpoints found, skipping status validation")
		return nil, fmt.Errorf("no broker endpoints available")
	}

	// try each endpoint until we get a successful response
	for _, addr := range addresses {
		status, err := v.getStatusFromEndpoint(ctx, addr)
		if err != nil {
			logger.Error(err, "Failed to get status from endpoint", "url", addr)
			continue
		}
		return status, nil
	}

	return nil, fmt.Errorf("failed to get status from any broker endpoint")
}

func (v *ServerValidator) getStatusFromEndpoint(ctx context.Context, url string) (*broker.StatusResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("received status %d", resp.StatusCode)
	}

	var status broker.StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &status, nil
}
