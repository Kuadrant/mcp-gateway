// main implements the CLI for the MCP broker/router.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof" //nolint:gosec // G108: intentional pprof endpoint for performance profiling
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/broker"
	"github.com/Kuadrant/mcp-gateway/internal/clients"
	config "github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/elicitation"
	"github.com/Kuadrant/mcp-gateway/internal/idmap"
	mcpRouter "github.com/Kuadrant/mcp-gateway/internal/mcp-router"
	mcpotel "github.com/Kuadrant/mcp-gateway/internal/otel"
	"github.com/Kuadrant/mcp-gateway/internal/session"
	goenv "github.com/caitlinelfring/go-env-default"
	"github.com/fsnotify/fsnotify"
	redis "github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
	"google.golang.org/grpc"
)

var (
	version = "dev"
	gitSHA  = "unknown"
	dirty   = ""
)

type commonConfig struct {
	logLevel              int
	logFormat             string
	gatewaySigningKey     string
	cacheConnectionString string
	sessionDurationMins   int64
	publicHost            string
	privateHost           string
	configFile            string
	enableURLElicitation  bool
	gatewayCACert         string
}

type routerConfig struct {
	// commonConfig is to be considered immutable
	commonConfig
	addr               string
	maxRequestBodySize int
}

type brokerConfig struct {
	// commonConfig is to be considered immutable
	commonConfig
	addr                       string
	writeTimeoutSecs           int64
	managerTickerIntervalSecs  int64
	enforceCapabilityFiltering bool
	invalidToolPolicy          string
	discoveryToolsEnabled      bool
	discoveryToolThreshold     int
	enablePprof                bool
}

type app struct {
	routerCfg      routerConfig
	brokerCfg      brokerConfig
	mcpConfig      *config.MCPServersConfig
	configMu       sync.RWMutex
	logger         *slog.Logger
	otelShutdown   func(context.Context) error
	redisClient    *redis.Client
	sessionCache   *session.Cache
	jwtMgr         *session.JWTManager
	elicitMap      idmap.Map
	tokenElicitMap elicitation.Map
	hairpinPool    *clients.HairpinClientPool
	mcpBroker      broker.MCPBroker
	tokenHandler   http.Handler
	elicitHandler  http.Handler
	brokerServer   *http.Server
	grpcServer     *grpc.Server
	router         *mcpRouter.ExtProcServer
}

func main() {
	ctx := context.Background()
	a := parseFlags()
	logOpts, jsonLog := a.setupLogger()
	a.setupTelemetry(ctx, logOpts, jsonLog)
	a.setupSessionInfra(ctx)
	a.buildHairpinClient()
	a.createBroker()
	a.createRouter()
	a.registerObservers()
	a.mcpConfig.MCPGatewayExternalHostname = a.brokerCfg.publicHost
	a.mcpConfig.MCPGatewayInternalHostname = a.brokerCfg.privateHost
	a.loadAndWatchConfig(ctx)
	a.run(ctx)
}

func parseFlags() *app {
	a := &app{
		mcpConfig: &config.MCPServersConfig{},
	}
	bc := &a.brokerCfg
	rc := &a.routerCfg

	// common flags — bound to brokerCfg.commonConfig, copied to routerCfg after parse
	flag.IntVar(&bc.logLevel, "log-level", int(slog.LevelInfo), "set the log level 0=info, 4=warn , 8=error and -4=debug")
	flag.StringVar(&bc.logFormat, "log-format", "txt", "switch to json logs with --log-format=json")
	flag.StringVar(&bc.publicHost, "mcp-gateway-public-host", "",
		"The public host the MCP Gateway is exposing MCP servers on. The gateway router will always set the :authority header to this value to ensure the broker component cannot be bypassed.")
	flag.StringVar(&bc.privateHost, "mcp-gateway-private-host",
		"mcp-gateway-istio.gateway-system.svc.cluster.local:8080",
		"The private host the MCP Gateway. The gateway router will use this to hairpin request to initialize MCP servers etc.")
	flag.StringVar(&bc.configFile, "mcp-gateway-config", "./config/samples/config.yaml", "where to locate the mcp server config")
	flag.Int64Var(&bc.sessionDurationMins, "session-length", 60*24, "default session length with the gateway in minutes. Default 24h")
	flag.BoolVar(&bc.enableURLElicitation, "enable-url-elicitation", false, "enable URL elicitation for per-user credential collection")
	flag.StringVar(&bc.gatewayCACert, "gateway-ca-cert", "",
		"path to a PEM CA certificate for the gateway's TLS listener (private CA support for hairpin requests)")

	gatewaySigningKeyDef := goenv.GetDefault("GATEWAY_SIGNING_KEY", "")
	if gatewaySigningKeyDef == "" {
		gatewaySigningKeyDef = goenv.GetDefault("JWT_SESSION_SIGNING_KEY", "")
	}
	flag.StringVar(&bc.gatewaySigningKey, "gateway-signing-key", gatewaySigningKeyDef,
		"Key used for JWT session signing and session cache encryption key derivation (env: GATEWAY_SIGNING_KEY or JWT_SESSION_SIGNING_KEY)")
	flag.StringVar(&bc.gatewaySigningKey, "session-signing-key", gatewaySigningKeyDef,
		"Deprecated alias for gateway-signing-key")
	flag.StringVar(&bc.cacheConnectionString, "cache-connection-string",
		goenv.GetDefault("CACHE_CONNECTION_STRING", ""),
		"redis based cache connection string redis://<user>:<pass>@localhost:6379/<db> (env: CACHE_CONNECTION_STRING). If not set defaults to  in memory storage")

	// broker-specific flags
	flag.StringVar(&bc.addr, "mcp-broker-public-address", "0.0.0.0:8080", "The public address for MCP broker")
	flag.Int64Var(&bc.writeTimeoutSecs, "mcp-broker-write-timeout", 0,
		"HTTP write timeout in seconds for the broker. Default 0 (disabled) for SSE notification support. Set > 0 to enable timeout.")
	flag.Int64Var(&bc.managerTickerIntervalSecs, "mcp-check-interval", 60, "interval in seconds for MCP manager backend health checks. Default 60 seconds.")
	flag.BoolVar(&bc.enforceCapabilityFiltering, "enforce-capability-filtering", false,
		"when enabled an x-mcp-authorized header will be needed to return any capabilities (tools, prompts)")
	flag.StringVar(&bc.invalidToolPolicy, "invalid-tool-policy", "FilterOut", "policy for upstream tools with invalid schemas: FilterOut (default) or RejectServer")
	flag.BoolVar(&bc.discoveryToolsEnabled, "discovery-tools-enabled", true,
		"enable discover_tools and select_tools meta-tools for progressive tool discovery")
	flag.IntVar(&bc.discoveryToolThreshold, "discovery-tool-threshold", 0,
		"tool count above which real tools are hidden and only meta-tools are shown. 0 means never hide.")
	flag.BoolVar(&bc.enablePprof, "enable-pprof", false, "enable pprof profiling server on localhost:6060")

	// router-specific flags
	flag.StringVar(&rc.addr, "mcp-router-address", "0.0.0.0:50051", "The address for MCP router")
	flag.IntVar(&rc.maxRequestBodySize, "max-request-body-size", 5242880, "max request body size in bytes for the ext_proc router. Default 5MB.")

	flag.Parse()

	if bc.publicHost == "" {
		panic("--mcp-gateway-public-host cannot be empty. The mcp gateway needs to be informed of what public host to expect requests from so it can ensure routing and session mgmt happens. Set --mcp-gateway-public-host")
	}

	// copy common config so router has its own snapshot
	rc.commonConfig = bc.commonConfig

	return a
}

func (a *app) setupLogger() (*slog.HandlerOptions, bool) {
	opts := &slog.HandlerOptions{}
	// flag value is a raw slog.Level (info=0, warn=4, error=8, debug=-4)
	opts.Level = slog.Level(a.brokerCfg.logLevel)

	jsonFormat := a.brokerCfg.logFormat == "json"
	a.logger = mcpotel.NewTracingLogger(os.Stdout, opts, jsonFormat, nil)
	return opts, jsonFormat
}

func (a *app) setupTelemetry(ctx context.Context, logOpts *slog.HandlerOptions, jsonFormat bool) {
	otelShutdown, loggerProvider, err := mcpotel.SetupOTelSDK(ctx, gitSHA, dirty, version, a.logger)
	if err != nil {
		a.logger.Error("failed to setup OpenTelemetry", "error", err)
	}
	a.otelShutdown = otelShutdown

	if loggerProvider != nil {
		a.logger = mcpotel.NewTracingLogger(os.Stdout, logOpts, jsonFormat, loggerProvider)
		a.logger.Info("Logger upgraded with OTLP export")
	}
}

func (a *app) setupSessionInfra(ctx context.Context) {
	if a.brokerCfg.cacheConnectionString != "" {
		a.logger.Info("cache using external redis store")
		redisOpt, err := redis.ParseURL(a.brokerCfg.cacheConnectionString)
		if err != nil {
			panic("failed to parse redis connection string: " + err.Error())
		}
		a.redisClient = redis.NewClient(redisOpt)
		if err := a.redisClient.Ping(ctx).Err(); err != nil {
			panic("failed to connect to redis: " + err.Error())
		}
	}

	var sessionCacheOpts []func(*session.Cache)
	sessionCacheOpts = append(sessionCacheOpts, session.WithRedisClient(a.redisClient))
	if a.redisClient != nil {
		encKey, err := session.DeriveEncryptionKey([]byte(a.brokerCfg.gatewaySigningKey))
		if err != nil {
			panic("failed to derive encryption key: " + err.Error())
		}
		sessionCacheOpts = append(sessionCacheOpts, session.WithEncryptionKey(encKey))
	}
	var err error
	a.sessionCache, err = session.NewCache(sessionCacheOpts...)
	if err != nil {
		panic("failed to setup session cache: " + err.Error())
	}

	if a.brokerCfg.gatewaySigningKey == "" {
		panic("GATEWAY_SIGNING_KEY (or JWT_SESSION_SIGNING_KEY) is required but not set. " +
			"When running via the controller, this is managed automatically. " +
			"For standalone use, set the GATEWAY_SIGNING_KEY environment variable.")
	}
	a.jwtMgr, err = session.NewJWTManager(a.brokerCfg.gatewaySigningKey, a.brokerCfg.sessionDurationMins, a.logger, a.sessionCache)
	if err != nil {
		panic("failed to setup jwt manager " + err.Error())
	}

	sessionTTL := time.Duration(a.brokerCfg.sessionDurationMins) * time.Minute
	a.elicitMap, err = idmap.New(idmap.WithRedisClient(a.redisClient), idmap.WithEntryTTL(sessionTTL))
	if err != nil {
		panic("failed to setup elicitation map: " + err.Error())
	}

	a.tokenElicitMap, err = elicitation.New(elicitation.WithRedisClient(a.redisClient))
	if err != nil {
		panic("failed to setup token elicitation map: " + err.Error())
	}
}

func (a *app) buildHairpinClient() {
	pool, err := clients.BuildHairpinHTTPClientPool(
		a.brokerCfg.privateHost,
		a.brokerCfg.publicHost,
		a.brokerCfg.gatewayCACert,
	)
	if err != nil {
		panic("failed to build hairpin HTTP client pool: " + err.Error())
	}
	a.hairpinPool = pool
}

func (a *app) registerObservers() {
	a.mcpConfig.RegisterObserver(a.router)
	a.mcpConfig.RegisterObserver(a.mcpBroker)
}

func (a *app) loadAndWatchConfig(ctx context.Context) {
	a.configMu.Lock()
	if err := a.loadConfig(a.brokerCfg.configFile); err != nil {
		panic("failed to load initial config: " + err.Error())
	}
	a.configMu.Unlock()
	a.mcpConfig.Notify(ctx)

	viper.SetOptions(viper.WithLogger(a.logger))
	viper.WatchConfig()
	viper.OnConfigChange(func(in fsnotify.Event) {
		a.logger.Info("config file changed", "config file", in.Name)
		a.configMu.Lock()
		defer a.configMu.Unlock()
		if err := a.loadConfig(a.brokerCfg.configFile); err != nil {
			a.logger.Error("failed to reload config, keeping previous", "error", err)
			return
		}
		a.logger.Info("notifying observers of config change")
		a.mcpConfig.Notify(ctx)
	})
}

func (a *app) run(ctx context.Context) {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)

	lc := net.ListenConfig{}
	lis, err := lc.Listen(ctx, "tcp", a.routerCfg.addr)
	if err != nil {
		a.logger.Error("grpc listen error", "error", err)
		stop <- os.Interrupt
	}
	if a.brokerCfg.enablePprof {
		go func() {
			pprofAddr := "localhost:6060"
			a.logger.Info("[pprof] starting profiling server", "listening", pprofAddr)
			pprofSrv := &http.Server{
				Addr:              pprofAddr,
				ReadHeaderTimeout: 5 * time.Second,
			}
			if err := pprofSrv.ListenAndServe(); err != nil {
				a.logger.Error("pprof server error", "error", err)
			}
		}()
	}

	go func() {
		a.logger.Info("[grpc] starting MCP Router", "listening", a.routerCfg.addr)
		if err := a.grpcServer.Serve(lis); err != nil {
			a.logger.Error("grpc server error", "error", err)
			stop <- os.Interrupt
		}
	}()

	go func() {
		a.logger.Info("[http] starting MCP Broker (public)", "listening", a.brokerServer.Addr)
		if err := a.brokerServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			a.logger.Error("http broker error", "error", err)
			stop <- os.Interrupt
		}
	}()

	<-stop

	a.logger.Info("shutting down MCP Broker and MCP Router")
	shutdownCtx, shutdownRelease := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownRelease()

	if a.otelShutdown != nil {
		if err := a.otelShutdown(shutdownCtx); err != nil {
			a.logger.Error("OpenTelemetry shutdown error", "error", err)
		}
	}

	if err := a.brokerServer.Shutdown(shutdownCtx); err != nil {
		a.logger.Error("HTTP shutdown error", "error", err)
	}
	if err := a.mcpBroker.Shutdown(shutdownCtx); err != nil {
		a.logger.Error("broker shutdown error", "error", err)
	}

	a.grpcServer.GracefulStop()

	if a.redisClient != nil {
		if err := a.redisClient.Close(); err != nil {
			a.logger.Error("redis close error", "error", err)
		}
	}
}

func (a *app) loadConfig(path string) error {
	viper.SetConfigFile(path)
	a.logger.Debug("loading config", "path", viper.ConfigFileUsed())
	if err := viper.ReadInConfig(); err != nil {
		return fmt.Errorf("reading config file: %w", err)
	}
	var newServers []*config.MCPServer
	if err := viper.UnmarshalKey("servers", &newServers); err != nil {
		return fmt.Errorf("decoding server config: %w", err)
	}
	var newVirtualServers []*config.VirtualServer
	if viper.IsSet("virtualServers") {
		if err := viper.UnmarshalKey("virtualServers", &newVirtualServers); err != nil {
			return fmt.Errorf("decoding virtualServers config: %w", err)
		}
	} else {
		a.logger.Debug("No virtualServers section found in configuration")
	}
	a.mcpConfig.SetServers(newServers, newVirtualServers)
	a.mcpConfig.SetGatewayCACertPEM(viper.GetString("gatewayCACertPEM"))

	a.logger.Debug("config successfully loaded", "# servers", len(newServers))

	for _, s := range newServers {
		a.logger.Debug(
			"server config",
			"server name",
			s.Name,
			"server prefix",
			s.Prefix,
			"state",
			s.State,
			"backend url",
			s.URL,
			"routable host",
			s.Hostname,
		)
	}
	return nil
}
