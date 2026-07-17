package controller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

// MCPGatewayExtensionValidator finds and validates MCPGatewayExtensions
type MCPGatewayExtensionValidator struct {
	client.Client
	DirectAPIReader client.Reader // uncached reader
	Logger          *slog.Logger
}

// HasValidReferenceGrant checks if a valid ReferenceGrant exists that allows the MCPGatewayExtension
// to reference a Gateway in a different namespace
func (r *MCPGatewayExtensionValidator) HasValidReferenceGrant(ctx context.Context, mcpExt *mcpv1.MCPGatewayExtension) (bool, error) {
	// list ReferenceGrants in the target Gateway's namespace
	refGrantList := &gatewayv1beta1.ReferenceGrantList{}
	if err := r.List(ctx, refGrantList, client.InNamespace(mcpExt.Spec.TargetRef.Namespace)); err != nil {
		return false, fmt.Errorf("failed to list ReferenceGrants: %w", err)
	}
	r.Logger.Debug("HasValidReferenceGrant found reference grants ", "len", len(refGrantList.Items))
	for _, rg := range refGrantList.Items {
		r.Logger.Debug("HasValidReferenceGrant checking reference grant ", "grant", rg.Name)
		if r.referenceGrantAllows(&rg, mcpExt) {
			return true, nil
		}
	}
	return false, nil
}

// referenceGrantAllows checks if a ReferenceGrant permits the MCPGatewayExtension to reference a Gateway
func (r *MCPGatewayExtensionValidator) referenceGrantAllows(rg *gatewayv1beta1.ReferenceGrant, mcpExt *mcpv1.MCPGatewayExtension) bool {
	fromAllowed := false
	toAllowed := false

	// check if 'from' allows MCPGatewayExtension from its namespace
	for _, from := range rg.Spec.From {
		if string(from.Group) == mcpv1.GroupVersion.Group &&
			string(from.Kind) == "MCPGatewayExtension" &&
			string(from.Namespace) == mcpExt.Namespace {
			fromAllowed = true
			break
		}
	}

	if !fromAllowed {
		return false
	}

	// check if 'to' allows Gateway references
	for _, to := range rg.Spec.To {
		// empty group means core, but Gateway is in gateway.networking.k8s.io
		if string(to.Group) == gatewayv1.GroupVersion.Group {
			// empty kind means all kinds in the group, or specific Gateway kind
			if to.Kind == "" || string(to.Kind) == "Gateway" {
				// if name is specified, it must match; empty means all
				if to.Name == nil || *to.Name == "" || string(*to.Name) == mcpExt.Spec.TargetRef.Name {
					toAllowed = true
					break
				}
			}
		}
	}

	return toAllowed
}

// FindValidMCPGatewayExtsForGateway will find all MCPGatewayExtensions indexed against passed Gateway instance
func (r *MCPGatewayExtensionValidator) FindValidMCPGatewayExtsForGateway(ctx context.Context, g *gatewayv1.Gateway) ([]*mcpv1.MCPGatewayExtension, error) {
	logger := logf.FromContext(ctx).WithName("findValidMCPGatewayExtsForGateway")
	validExtensions := []*mcpv1.MCPGatewayExtension{}
	mcpGatewayExtList := &mcpv1.MCPGatewayExtensionList{}
	if err := r.List(ctx, mcpGatewayExtList,
		client.MatchingFields{gatewayIndexKey: gatewayToMCPExtIndexValue(*g)},
	); err != nil {
		return validExtensions, err
	}
	logger.V(1).Info("found mcpgatewayextensions", "total", len(mcpGatewayExtList.Items))
	for _, mg := range mcpGatewayExtList.Items {
		if mg.DeletionTimestamp != nil {
			logger.V(1).Info("found deleting mcpgatewayextensions not including as not valid", "name", mg.Name)
			continue
		}

		if mg.Namespace == g.Namespace {
			validExtensions = append(validExtensions, &mg)
			continue
		}
		has, err := r.HasValidReferenceGrant(ctx, &mg)
		if err != nil {
			// we have to exit here
			return validExtensions, fmt.Errorf("failed to check if mcpgatewayextension is valid %w", err)
		}
		if has && meta.IsStatusConditionTrue(mg.Status.Conditions, mcpv1.ConditionTypeReady) {
			validExtensions = append(validExtensions, &mg)
		}
	}
	return validExtensions, nil
}

// MCPGatewayExtensionFinderValidator finds and validates MCPGatewayExtensions
type MCPGatewayExtensionFinderValidator interface {
	HasValidReferenceGrant(ctx context.Context, mcpExt *mcpv1.MCPGatewayExtension) (bool, error)
	FindValidMCPGatewayExtsForGateway(ctx context.Context, g *gatewayv1.Gateway) ([]*mcpv1.MCPGatewayExtension, error)
}

// ListMCPGatewayExtensionNamespaces returns the namespace of every active MCPGatewayExtension.
// VirtualServer config must be written to all of them so every gateway instance receives the
// full virtual server list regardless of which namespace the MCPVirtualServer lives in.
func (r *MCPGatewayExtensionValidator) ListMCPGatewayExtensionNamespaces(ctx context.Context) ([]string, error) {
	list := &mcpv1.MCPGatewayExtensionList{}
	if err := r.List(ctx, list); err != nil {
		return nil, fmt.Errorf("failed to list MCPGatewayExtensions: %w", err)
	}
	namespaces := make([]string, 0, len(list.Items))
	for i := range list.Items {
		if list.Items[i].DeletionTimestamp != nil {
			continue
		}
		namespaces = append(namespaces, list.Items[i].Namespace)
	}
	return namespaces, nil
}

// httpRouteAttachesToListener checks whether an HTTPRoute is attached to the listener
// targeted by an MCPGatewayExtension. An HTTPRoute attaches to a listener if:
//  1. The HTTPRoute parentRef has a sectionName that resolves to a listener sharing
//     the same port as the extension's target listener, OR
//  2. The HTTPRoute has no sectionName but its hostnames match a listener hostname
//     on the same port
func httpRouteAttachesToListener(route *gatewayv1.HTTPRoute, gateway *gatewayv1.Gateway, ext *mcpv1.MCPGatewayExtension) bool {
	// find the port for the extension's target listener
	targetPort, ok := listenerPort(gateway, ext.Spec.TargetRef.SectionName)
	if !ok {
		return false
	}

	// collect all listener hostnames on the same port
	samePortHostnames := listenersHostnamesByPort(gateway, targetPort)
	// collect all listener names on the same port
	samePortNames := listenerNamesByPort(gateway, targetPort)

	for _, parentRef := range route.Spec.ParentRefs {
		if !parentRefMatchesGateway(parentRef, gateway, route.Namespace) {
			continue
		}
		if parentRef.SectionName != nil {
			// explicit sectionName: check if it targets a listener on the same port
			if samePortNames[string(*parentRef.SectionName)] {
				return true
			}
			continue
		}
		// no sectionName: match by hostname
		for _, routeHostname := range route.Spec.Hostnames {
			for _, listenerHostname := range samePortHostnames {
				if hostnameMatches(string(routeHostname), listenerHostname) {
					return true
				}
			}
		}
	}
	return false
}

// listenerPort returns the port for a named listener on the gateway
func listenerPort(gateway *gatewayv1.Gateway, sectionName string) (gatewayv1.PortNumber, bool) {
	for _, l := range gateway.Spec.Listeners {
		if string(l.Name) == sectionName {
			return l.Port, true
		}
	}
	return 0, false
}

// listenersHostnamesByPort returns all hostnames from listeners on the given port.
// listeners without a hostname are skipped (invalid for MCP gateway).
func listenersHostnamesByPort(gateway *gatewayv1.Gateway, port gatewayv1.PortNumber) []string {
	var hostnames []string
	for _, l := range gateway.Spec.Listeners {
		if l.Port == port && l.Hostname != nil && *l.Hostname != "" {
			hostnames = append(hostnames, string(*l.Hostname))
		}
	}
	return hostnames
}

// listenerNamesByPort returns a set of listener names that share the given port
func listenerNamesByPort(gateway *gatewayv1.Gateway, port gatewayv1.PortNumber) map[string]bool {
	names := map[string]bool{}
	for _, l := range gateway.Spec.Listeners {
		if l.Port == port {
			names[string(l.Name)] = true
		}
	}
	return names
}

// parentRefMatchesGateway checks if a parentRef points to the given gateway
func parentRefMatchesGateway(ref gatewayv1.ParentReference, gateway *gatewayv1.Gateway, routeNamespace string) bool {
	if ref.Group != nil && string(*ref.Group) != gatewayv1.GroupVersion.Group {
		return false
	}
	if ref.Kind != nil && string(*ref.Kind) != "Gateway" {
		return false
	}
	if string(ref.Name) != gateway.Name {
		return false
	}
	ns := routeNamespace
	if ref.Namespace != nil {
		ns = string(*ref.Namespace)
	}
	return ns == gateway.Namespace
}

// hostnameMatches checks if a route hostname matches a listener hostname pattern.
// supports wildcard listeners (e.g. *.team-a.mcp.local matches server1.team-a.mcp.local)
func hostnameMatches(routeHostname, listenerHostname string) bool {
	if listenerHostname == routeHostname {
		return true
	}
	// wildcard listener: *.example.com matches foo.example.com
	if strings.HasPrefix(listenerHostname, "*.") {
		suffix := listenerHostname[1:] // ".example.com"
		// route hostname must have exactly one more label
		if strings.HasSuffix(routeHostname, suffix) {
			prefix := routeHostname[:len(routeHostname)-len(suffix)]
			// prefix must be a single label (no dots)
			if len(prefix) > 0 && !strings.Contains(prefix, ".") {
				return true
			}
		}
	}
	return false
}
