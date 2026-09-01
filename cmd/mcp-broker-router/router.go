package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"github.com/Kuadrant/mcp-gateway/internal/clients"
	"github.com/Kuadrant/mcp-gateway/internal/guardrails"
	mcpRouter "github.com/Kuadrant/mcp-gateway/internal/mcp-router"
	"github.com/Kuadrant/mcp-gateway/internal/routing"
	extProcV3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
)

func (a *app) createRouter() {
	cfg := &a.routerCfg

	a.grpcServer = grpc.NewServer()
	a.server = &mcpRouter.ExtProcServer{
		Logger:             a.logger.With("component", "router"),
		SessionCache:       a.sessionCache,
		ElicitationMap:     a.elicitMap,
		MaxRequestBodySize: cfg.maxRequestBodySize,
		EnableA2A:          cfg.enableA2A,
	}

	if a.mcpConfig == nil {
		panic("mcpConfig must be non-nil before constructing the ext_proc server")
	}
	a.server.RoutingConfig.Store(a.mcpConfig)

	a.server.Router202607 = &routing.Router202607{
		Table:         a.mcpBroker.RoutingTable,
		RoutingConfig: &a.server.RoutingConfig,
		Logger:        a.logger.With("component", "router-202607"),
	}
	a.server.ResponseHandler2026 = &routing.ResponseHandler202607{
		Logger: a.logger.With("component", "response-handler-202607"),
	}

	a.server.Router = &routing.Router202511{
		RoutingConfig:       &a.server.RoutingConfig,
		Table:               a.mcpBroker.RoutingTable,
		SessionCache:        a.sessionCache,
		JWTManager:          a.jwtMgr,
		InitForClient:       clients.Initialize,
		HairpinClientPool:   a.hairpinPool,
		ElicitationMap:      a.elicitMap,
		TokenElicitationMap: a.tokenElicitMap,
		ElicitationEnabled:  cfg.enableURLElicitation,
		Logger:              a.logger.With("component", "router-202511"),
	}

	a.server.ResponseHandler = &routing.ResponseHandler202511{
		RoutingConfig:      &a.server.RoutingConfig,
		SessionCache:       a.sessionCache,
		JWTManager:         a.jwtMgr,
		ElicitationEnabled: cfg.enableURLElicitation,
		Logger:             a.logger.With("component", "response-handler-202511"),
	}

	extProcV3.RegisterExternalProcessorServer(a.grpcServer, a.server)
}

// rebuildGuardrailsChecker recreates the guardrails Checker from the latest
// GlobalGuardrails config and gateway CA bundle, storing it on mcpConfig so
// both routers see it via RoutingConfig. Nil when guardrails is not configured.
func (a *app) rebuildGuardrailsChecker(ctx context.Context) {
	if a.mcpConfig == nil || a.mcpConfig.GetGlobalGuardrails() == nil {
		if a.mcpConfig != nil && a.mcpConfig.GetGuardrailsChecker() != nil {
			a.mcpConfig.SetGuardrailsChecker(nil)
			a.logger.InfoContext(ctx, "guardrails checker destroyed")
		}
		return
	}

	tlsConfig, err := tlsConfigFromCACertPEM(a.mcpConfig.GetGatewayCACertPEM())
	if err != nil {
		a.logger.ErrorContext(ctx, "failed to build guardrails TLS config", "error", err)
		a.mcpConfig.SetGuardrailsChecker(nil)
		return
	}

	a.mcpConfig.SetGuardrailsChecker(guardrails.NewChecker(a.mcpConfig.GetGlobalGuardrails(), tlsConfig, 0, a.mcpConfig.GetMaxBodyBytes()))
	a.logger.InfoContext(ctx, "guardrails checker created")
}

func tlsConfigFromCACertPEM(caCertPEM string) (*tls.Config, error) {
	certPool, err := x509.SystemCertPool()
	if err != nil {
		certPool = x509.NewCertPool()
	}
	if caCertPEM != "" && !certPool.AppendCertsFromPEM([]byte(caCertPEM)) {
		return nil, fmt.Errorf("failed to parse gateway CA cert PEM")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    certPool,
	}, nil
}
