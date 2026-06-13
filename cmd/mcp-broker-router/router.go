package main

import (
	"github.com/Kuadrant/mcp-gateway/internal/clients"
	mcpRouter "github.com/Kuadrant/mcp-gateway/internal/mcp-router"
	extProcV3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
)

func (a *app) createRouter() {
	cfg := &a.routerCfg
	a.grpcServer = grpc.NewServer()
	a.router = &mcpRouter.ExtProcServer{
		Logger:              a.logger.With("component", "router"),
		JWTManager:          a.jwtMgr,
		InitForClient:       clients.Initialize,
		HairpinClientPool:   a.hairpinPool,
		SessionCache:        a.sessionCache,
		ElicitationMap:      a.elicitMap,
		TokenElicitationMap: a.tokenElicitMap,
		Broker:              a.mcpBroker, // TODO we shouldn't need a handle to broker in the router
		MaxRequestBodySize:  cfg.maxRequestBodySize,
		ElicitationEnabled:  cfg.enableURLElicitation,
	}
	// seed initial routing config so reader sites never observe a nil pointer before the first OnConfigChange fires.
	if a.mcpConfig == nil {
		panic("mcpConfig must be non-nil before constructing the ext_proc server")
	}
	a.router.RoutingConfig.Store(a.mcpConfig)

	extProcV3.RegisterExternalProcessorServer(a.grpcServer, a.router)
}
