package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/broker"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

func (a *app) createBroker() {
	invalidToolPolicy := mcpv1.InvalidToolPolicy(a.brokerCfg.invalidToolPolicy)
	if invalidToolPolicy != mcpv1.InvalidToolPolicyFilterOut && invalidToolPolicy != mcpv1.InvalidToolPolicyRejectServer {
		panic("--invalid-tool-policy must be FilterOut or RejectServer")
	}

	managerTickerInterval := time.Duration(a.brokerCfg.managerTickerIntervalSecs) * time.Second
	if managerTickerInterval <= 0 {
		panic("flag mcp-check-interval cannot be 0 or less seconds")
	}

	brokerOpts := []broker.Option{
		broker.WithEnforceCapabilityFilter(a.brokerCfg.enforceCapabilityFiltering),
		broker.WithTrustedHeadersPublicKey(os.Getenv("TRUSTED_HEADER_PUBLIC_KEY")),
		broker.WithManagerTickerInterval(managerTickerInterval),
		broker.WithInvalidToolPolicy(invalidToolPolicy),
		broker.WithElicitationEnabled(a.brokerCfg.enableURLElicitation),
		broker.WithDiscoveryToolsEnabled(a.brokerCfg.discoveryToolsEnabled),
		broker.WithDiscoveryToolThreshold(a.brokerCfg.discoveryToolThreshold),
		broker.WithSessionCache(a.sessionCache),
	}
	if a.jwtMgr != nil {
		brokerOpts = append(brokerOpts,
			broker.WithSessionIDGenerator(a.jwtMgr.Generate),
			broker.WithSessionValidator(a.jwtMgr.Validate),
			broker.WithSessionTerminator(a.jwtMgr.Terminate),
		)
	}
	a.mcpBroker = broker.NewBroker(a.logger.With("component", "broker"), brokerOpts...)
	a.tokenHandler = broker.NewTokenHandler(a.sessionCache, a.tokenElicitMap, *a.logger)
	a.elicitHandler = &broker.ElicitationHandler{
		ElicitationMap: a.tokenElicitMap,
		Config:         a.mcpConfig,
	}
	a.setUpHTTPServer()
}

func (a *app) setUpHTTPServer() {
	cfg := &a.brokerCfg
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "Hello, World!  BTW, the MCP server is on /mcp")
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !a.mcpBroker.IsReady() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	oauthHandler := broker.ProtectedResourceHandler{Logger: a.logger}
	mux.HandleFunc("/.well-known/oauth-protected-resource", oauthHandler.Handle)
	mux.HandleFunc("/.well-known/oauth-protected-resource/", oauthHandler.Handle)

	// WriteTimeout of 0 (disabled) is important for SSE connections (GET /mcp).
	// SSE streams notifications indefinitely - any write timeout would kill the connection.
	writeTimeout := time.Duration(cfg.writeTimeoutSecs) * time.Second

	httpSrv := &http.Server{
		Addr:         cfg.addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: writeTimeout,
	}

	mux.HandleFunc("OPTIONS /mcp", func(w http.ResponseWriter, r *http.Request) {
		a.logger.Debug("Handling OPTIONS", "Mcp-Session-Id", r.Header.Get("Mcp-Session-Id"))
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/status", a.mcpBroker.HandleStatusRequest)
	mux.HandleFunc("/status/", a.mcpBroker.HandleStatusRequest)
	if cfg.enableURLElicitation {
		mux.Handle("/tokens", a.tokenHandler)
		mux.Handle("/mcp/elicitation", a.elicitHandler)
	}
	mux.Handle("/mcp", traceContextMiddleware(a.mcpBroker.MCPHandler()))

	a.brokerServer = httpSrv
}

func traceContextMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
