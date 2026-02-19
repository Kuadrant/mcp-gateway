// main implements the CLI for the MCP broker/router.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/broker"
	"github.com/Kuadrant/mcp-gateway/internal/clients"
	config "github.com/Kuadrant/mcp-gateway/internal/config"
	mcpRouter "github.com/Kuadrant/mcp-gateway/internal/mcp-router"
	mcpotel "github.com/Kuadrant/mcp-gateway/internal/otel"
	"github.com/Kuadrant/mcp-gateway/internal/session"
	goenv "github.com/caitlinelfring/go-env-default"
	extProcV3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/fsnotify/fsnotify"
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

var (
	version = "dev"
	gitSHA  = "unknown"
	dirty   = ""
)

var (
	mcpConfig            = &config.MCPServersConfig{}
	mutex                sync.RWMutex
	logger               = slog.New(slog.NewTextHandler(os.Stdout, nil))
	scheme               = runtime.NewScheme()
	defaultJWTSigningKey = "default-not-secure"
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = mcpv1alpha1.AddToScheme(scheme)
	_ = gatewayv1.Install(scheme)
}

var (
	mcpRouterAddrFlag         string
	mcpBrokerAddrFlag         string
	mcpRoutePublicHost        string
	mcpRoutePrivateHost       string
	mcpRouterKey              string
	cacheConnectionStringFlag string
	mcpConfigFile             string
	jwtSigningKeyFlag         string
	sessionDurationInMins     int64
	brokerWriteTimeoutSecs    int64
	managerTickerIntervalSecs int64
	loglevel                  int
	logFormat                 string
	enforceToolFilteringFlag  bool
)

func main() {

	flag.StringVar(
		&mcpRouterAddrFlag,
		"mcp-router-address",
		"0.0.0.0:50051",
		"The address for MCP router",
	)
	flag.StringVar(
		&mcpBrokerAddrFlag,
		"mcp-broker-public-address",
		"0.0.0.0:8080",
		"The public address for MCP broker",
	)
	flag.StringVar(
		&mcpRoutePublicHost,
		"mcp-gateway-public-host",
		"",
		"The public host the MCP Gateway is exposing MCP servers on. The gateway router will always set the :authority header to this value to ensure the broker component cannot be bypassed.",
	)
	flag.StringVar(
		&mcpRoutePrivateHost,
		"mcp-gateway-private-host",
		"mcp-gateway-istio.gateway-system.svc.cluster.local:8080",
		"The private host the MCP Gateway. The gateway router will use this to hairpin request to initialize MCP servers etc.",
	)

	// TODO ick not sure how to describe this
	flag.StringVar(
		&mcpRouterKey,
		"mcp-router-key",
		goenv.GetDefault("MCP_ROUTER_API_KEY", "secret-api-key"),
		"this key is used to allow the router to send request through the gateway and be trusted by the router",
	)
	flag.StringVar(
		&mcpConfigFile,
		"mcp-gateway-config",
		"./config/mcp-system/config.yaml",
		"where to locate the mcp server config",
	)
	flag.IntVar(
		&loglevel,
		"log-level",
		int(slog.LevelInfo),
		"set the log level 0=info, 4=warn , 8=error and -4=debug",
	)
	flag.StringVar(&jwtSigningKeyFlag,
		"session-signing-key",
		goenv.GetDefault("JWT_SESSION_SIGNING_KEY", defaultJWTSigningKey),
		"JWT signing key for session tokens (env: JWT_SESSION_SIGNING_KEY)",
	)
	//"redis://redis.mcp-system.svc.cluster.local:6379
	flag.StringVar(&cacheConnectionStringFlag,
		"cache-connection-string",
		goenv.GetDefault("CACHE_CONNECTION_STRING", ""),
		"redis based cache connection string redis://<user>:<pass>@localhost:6379/<db> (env: CACHE_CONNECTION_STRING). If not set defaults to  in memory storage",
	)
	flag.StringVar(&logFormat, "log-format", "txt", "switch to json logs with --log-format=json")

	flag.Int64Var(&sessionDurationInMins, "session-length", 60*24, "default session length with the gateway in minutes. Default 24h")
	flag.Int64Var(&brokerWriteTimeoutSecs, "mcp-broker-write-timeout", 0, "HTTP write timeout in seconds for the broker. Default 0 (disabled) for SSE notification support. Set > 0 to enable timeout.")
	flag.Int64Var(&managerTickerIntervalSecs, "mcp-check-interval", 60, "interval in seconds for MCP manager backend health checks. Default 60 seconds.")
	flag.BoolVar(&enforceToolFilteringFlag, "enforce-tool-filtering", false, "when enabled an x-authorized-tools header will be needed to return any tools")
	flag.Parse()

	loggerOpts := &slog.HandlerOptions{}

	switch loglevel {
	case 0:
		loggerOpts.Level = slog.LevelInfo
	case 8:
		loggerOpts.Level = slog.LevelError
	case -4:
		loggerOpts.Level = slog.LevelDebug
	default:
		loggerOpts.Level = slog.LevelDebug
	}

	jsonFormat := logFormat == "json"
	logger = mcpotel.NewTracingLogger(os.Stdout, loggerOpts, jsonFormat, nil)

	ctx := context.Background()

	otelShutdown, loggerProvider, err := mcpotel.SetupOTelSDK(ctx, gitSHA, dirty, version, logger)
	if err != nil {
		logger.Error("failed to setup OpenTelemetry", "error", err)
	}

	if loggerProvider != nil {
		logger = mcpotel.NewTracingLogger(os.Stdout, loggerOpts, jsonFormat, loggerProvider)
		logger.Info("Logger upgraded with OTLP export")
	}

	sessionCache, err := session.NewCache(ctx)
	if err != nil {
		panic("failed to setup session cache" + err.Error())
	}
	if cacheConnectionStringFlag != "" {
		logger.Info("session cache using external store")
		sessionCache, err = session.NewCache(ctx, session.WithConnectionString(cacheConnectionStringFlag))
		if err != nil {
			panic("failed to setup session cache" + err.Error())
		}
	}

	var jwtSessionMgr *session.JWTManager
	if jwtSigningKeyFlag == "" {
		panic("jwt session signing key is empty. Cannot proceed")
	}
	if jwtSigningKeyFlag == defaultJWTSigningKey {
		logger.Warn("jwt session signing key is set to the default value. This is not recommended for production")
	}

	jwtmgr, err := session.NewJWTManager(jwtSigningKeyFlag, sessionDurationInMins, logger, sessionCache)
	if err != nil {
		panic("failed to setup jwt manager " + err.Error())
	}
	jwtSessionMgr = jwtmgr

	managerTickerInterval := time.Duration(managerTickerIntervalSecs) * time.Second
	brokerServer, mcpBroker, mcpServer := setUpBroker(mcpBrokerAddrFlag, enforceToolFilteringFlag, jwtSessionMgr, brokerWriteTimeoutSecs, managerTickerInterval)
	routerGRPCServer, router := setUpRouter(mcpBroker, logger, jwtSessionMgr, sessionCache)
	mcpConfig.RegisterObserver(router)
	mcpConfig.RegisterObserver(mcpBroker)
	if mcpRoutePublicHost == "" {
		panic("--mcp-gateway-public-host cannot be empty. The mcp gateway needs to be informed of what public host to expect requests from so it can ensure routing and session mgmt happens. Set --mcp-gateway-public-host")
	}

	mcpConfig.MCPGatewayExternalHostname = mcpRoutePublicHost
	mcpConfig.MCPGatewayInternalHostname = mcpRoutePrivateHost
	mcpConfig.RouterAPIKey = mcpRouterKey

	// Only load config and run broker/router in standalone mode
	mutex.Lock()
	// will panic if fails
	LoadConfig(mcpConfigFile)
	mutex.Unlock()
	mcpConfig.Notify(ctx)

	viper.WatchConfig()
	// set up our change event handler
	viper.OnConfigChange(func(in fsnotify.Event) {
		logger.Info("OnConfigChange mcp servers config changed ", "config file", in.Name)
		mutex.Lock()
		defer mutex.Unlock()
		LoadConfig(mcpConfigFile)
		logger.Info("OnConfigChange: notifying observers of config change")
		mcpConfig.Notify(ctx)
	})
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)

	grpcAddr := mcpRouterAddrFlag
	lc := net.ListenConfig{}
	lis, err := lc.Listen(ctx, "tcp", grpcAddr)
	if err != nil {
		log.Fatalf("[grpc] listen error: %v", err)
	}

	go func() {
		logger.Info("[grpc] starting MCP Router", "listening", grpcAddr)
		log.Fatal(routerGRPCServer.Serve(lis))
	}()

	go func() {
		logger.Info("[http] starting MCP Broker (public)", "listening", brokerServer.Addr)
		if err := brokerServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[http] Cannot start public broker: %v", err)
		}
	}()

	<-stop
	// handle shutdown
	logger.Info("shutting down MCP Broker and MCP Router")

	shutdownCtx, shutdownRelease := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownRelease()

	if err := otelShutdown(shutdownCtx); err != nil {
		logger.Error("OpenTelemetry shutdown error", "error", err)
	}

	if err := brokerServer.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("HTTP shutdown error: %v", err)
	}
	if err := mcpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("MCP shutdown error: %v; ignoring", err)
	}

	routerGRPCServer.GracefulStop()
}

func setUpBroker(address string, toolFiltering bool, sessionManager *session.JWTManager, writeTimeoutSecs int64, managerTickerInterval time.Duration) (*http.Server, broker.MCPBroker, *server.StreamableHTTPServer) {

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "Hello, World!  BTW, the MCP server is on /mcp")
	})

	// Add OAuth protected resource endpoint
	oauthHandler := broker.ProtectedResourceHandler{Logger: logger}
	mux.HandleFunc("/.well-known/oauth-protected-resource", oauthHandler.Handle)

	// WriteTimeout of 0 (disabled) is important for SSE connections (GET /mcp).
	// SSE streams notifications indefinitely - any write timeout would kill the connection.
	// Connection lifecycle is managed by the application (client disconnect, session expiry, etc.)
	writeTimeout := time.Duration(writeTimeoutSecs) * time.Second

	httpSrv := &http.Server{
		Addr:         address,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: writeTimeout,
	}

	if managerTickerInterval <= 0 {
		panic("flag mcp-check-interval cannot be 0 or less seconds")
	}
	mcpBroker := broker.NewBroker(logger.With("component", "broker"),
		broker.WithEnforceToolFilter(toolFiltering),
		broker.WithTrustedHeadersPublicKey(os.Getenv("TRUSTED_HEADER_PUBLIC_KEY")),
		broker.WithManagerTickerInterval(managerTickerInterval),
	)

	var streamableHTTPServer = server.NewStreamableHTTPServer(
		mcpBroker.MCPServer(),
		server.WithStreamableHTTPServer(httpSrv),
	)
	if sessionManager != nil {
		logger.Info("jwt session manager configured")
		streamableHTTPServer = server.NewStreamableHTTPServer(
			mcpBroker.MCPServer(),
			server.WithStreamableHTTPServer(httpSrv),
			server.WithSessionIdManager(sessionManager),
		)
	}

	// Allow direct connections with MCP Inspector
	mux.HandleFunc("OPTIONS /mcp", func(w http.ResponseWriter, r *http.Request) {
		logger.Debug("Handling OPTIONS", "Mcp-Session-Id", r.Header.Get("Mcp-Session-Id"))
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("/status", mcpBroker.HandleStatusRequest)
	mux.HandleFunc("/status/", mcpBroker.HandleStatusRequest)
	mux.Handle("/mcp", streamableHTTPServer)

	return httpSrv, mcpBroker, streamableHTTPServer
}

func setUpRouter(broker broker.MCPBroker, logger *slog.Logger, jwtManager *session.JWTManager, sessionCache *session.Cache) (*grpc.Server, *mcpRouter.ExtProcServer) {

	grpcSrv := grpc.NewServer()
	// Create the ExtProcServer instance
	server := &mcpRouter.ExtProcServer{
		RoutingConfig: mcpConfig,
		Logger:        logger.With("component", "router"),
		JWTManager:    jwtManager,
		InitForClient: clients.Initialize,
		SessionCache:  sessionCache,
		Broker:        broker, // TODO we shouldn't need a handle to broker in the router

	}

	extProcV3.RegisterExternalProcessorServer(grpcSrv, server)
	return grpcSrv, server
}

// config

func LoadConfig(path string) {
	viper.SetConfigFile(path)
	logger.Debug("loading config", "path", viper.ConfigFileUsed())
	err := viper.ReadInConfig()
	if err != nil {
		log.Fatalf("Error reading config file: %s", err)
	}
	// reset the servers to avoid old configs being written to
	mcpConfig.Servers = []*config.MCPServer{}
	err = viper.UnmarshalKey("servers", &mcpConfig.Servers)
	if err != nil {
		log.Fatalf("Unable to decode server config into struct: %s", err)
	}
	mcpConfig.VirtualServers = []*config.VirtualServer{}
	// Load virtualServers if present - this is optional
	if viper.IsSet("virtualServers") {
		err = viper.UnmarshalKey("virtualServers", &mcpConfig.VirtualServers)
		if err != nil {
			log.Fatal("Failed to parse virtualServers configuration", "error", err)
		}
	} else {
		logger.Debug("No virtualServers section found in configuration")
	}

	logger.Debug("config successfully loaded", "# servers", len(mcpConfig.Servers))

	for _, s := range mcpConfig.Servers {
		logger.Debug(
			"server config",
			"server name",
			s.Name,
			"server prefix",
			s.ToolPrefix,
			"enabled",
			s.Enabled,
			"backend url",
			s.URL,
			"routable host",
			s.Hostname,
		)
	}
}
