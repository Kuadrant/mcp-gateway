package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"github.com/Kuadrant/mcp-gateway/internal/clients"
	mcpRouter "github.com/Kuadrant/mcp-gateway/internal/mcp-router"
	"github.com/Kuadrant/mcp-gateway/internal/routing"
	extProcV3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
)

// grpcMaxRecvMsgSize is the ext_proc receive limit. 16 MiB leaves room above
// the 5 MiB maxBodyBytes default and covers the documented 10 MiB example,
// plus the ext_proc framing carried with a BUFFERED body. grpc's 4 MiB
// default would reject those bodies before the router sees them.
const grpcMaxRecvMsgSize = 16 << 20

func newGRPCServer() *grpc.Server {
	return grpc.NewServer(grpc.MaxRecvMsgSize(grpcMaxRecvMsgSize))
}

func (a *app) createGRPCServer() {
	a.grpcServer = newGRPCServer()
	extProcV3.RegisterExternalProcessorServer(a.grpcServer, a.server)
}

func (a *app) createRouter() {
	cfg := &a.routerCfg

	a.server = &mcpRouter.ExtProcServer{
		Logger:         a.logger.With("component", "router"),
		SessionCache:   a.sessionCache,
		ElicitationMap: a.elicitMap,
		EnableA2A:      cfg.enableA2A,
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
		RoutingConfig: &a.server.RoutingConfig,
		Logger:        a.logger.With("component", "response-handler-202607"),
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
