// Package controller holds the code for the kubernetes controllers
package controller

import (
	"fmt"
	"strings"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// HTTPRouteWrapper provides helper methods for inspecting HTTPRoute resources
type HTTPRouteWrapper struct {
	*gatewayv1.HTTPRoute
}

// WrapHTTPRoute creates a new HTTPRouteWrapper
func WrapHTTPRoute(route *gatewayv1.HTTPRoute) *HTTPRouteWrapper {
	return &HTTPRouteWrapper{HTTPRoute: route}
}

// Validate checks if the HTTPRoute has a valid structure for MCP processing
func (w *HTTPRouteWrapper) Validate() error {
	if len(w.Spec.Rules) == 0 || len(w.Spec.Rules[0].BackendRefs) == 0 {
		return fmt.Errorf("HTTPRoute %s/%s has no backend references", w.Namespace, w.Name)
	}
	if len(w.Spec.Rules) > 1 {
		return fmt.Errorf("HTTPRoute %s/%s has > 1 rule, which is unsupported", w.Namespace, w.Name)
	}
	if len(w.Spec.Rules[0].BackendRefs) > 1 {
		return fmt.Errorf("HTTPRoute %s/%s has > 1 backend reference, which is unsupported", w.Namespace, w.Name)
	}
	if len(w.Spec.Hostnames) == 0 {
		return fmt.Errorf("HTTPRoute %s/%s must have at least one hostname", w.Namespace, w.Name)
	}
	if w.BackendRef().Name == "" {
		return fmt.Errorf("HTTPRoute %s/%s backend reference has no name", w.Namespace, w.Name)
	}
	return nil
}

// BackendRef returns the first backend reference
func (w *HTTPRouteWrapper) BackendRef() gatewayv1.HTTPBackendRef {
	return w.Spec.Rules[0].BackendRefs[0]
}

// FirstHostname returns the first hostname
func (w *HTTPRouteWrapper) FirstHostname() string {
	return string(w.Spec.Hostnames[0])
}

// BackendKind returns the backend kind, defaulting to "Service"
func (w *HTTPRouteWrapper) BackendKind() string {
	if w.BackendRef().Kind != nil {
		return string(*w.BackendRef().Kind)
	}
	return "Service"
}

// BackendGroup returns the backend group
func (w *HTTPRouteWrapper) BackendGroup() string {
	if w.BackendRef().Group != nil {
		return string(*w.BackendRef().Group)
	}
	return ""
}

// BackendName returns the backend name as string
func (w *HTTPRouteWrapper) BackendName() string {
	return string(w.BackendRef().Name)
}

// BackendNamespace returns the backend namespace, defaulting to HTTPRoute namespace
func (w *HTTPRouteWrapper) BackendNamespace() string {
	if w.BackendRef().Namespace != nil {
		return string(*w.BackendRef().Namespace)
	}
	return w.Namespace
}

// BackendPort returns the backend port, or nil if not specified
func (w *HTTPRouteWrapper) BackendPort() *gatewayv1.PortNumber {
	return w.BackendRef().Port
}

// IsHostnameBackend returns true if the backend is an Istio Hostname type
func (w *HTTPRouteWrapper) IsHostnameBackend() bool {
	return w.BackendKind() == "Hostname" && w.BackendGroup() == "networking.istio.io"
}

// IsServiceBackend returns true if the backend is a Service
func (w *HTTPRouteWrapper) IsServiceBackend() bool {
	return w.BackendKind() == "Service"
}

// UsesHTTPS checks if any parent ref section name contains "https"
func (w *HTTPRouteWrapper) UsesHTTPS() bool {
	for _, parentRef := range w.Spec.ParentRefs {
		if parentRef.SectionName != nil && strings.Contains(string(*parentRef.SectionName), "https") {
			return true
		}
	}
	return false
}
