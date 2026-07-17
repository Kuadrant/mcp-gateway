package main

import (
	"github.com/Kuadrant/mcp-gateway/internal/clients"
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
	}

	if a.mcpConfig == nil {
		panic("mcpConfig must be non-nil before constructing the ext_proc server")
	}
	a.server.RoutingConfig.Store(a.mcpConfig)

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
