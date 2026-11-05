// Package redisconfig parses session-store settings shared by the controller and broker-router.
package redisconfig

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	redis "github.com/redis/go-redis/v9"
)

// keys are the session-store Secret keys exposed to the broker-router.
var keys = []string{
	"CACHE_MODE",
	"CACHE_CONNECTION_STRING",
	"CACHE_SENTINEL_MASTER",
	"CACHE_SENTINEL_ADDRS",
	"CACHE_SENTINEL_USERNAME",
	"CACHE_SENTINEL_PASSWORD",
	"CACHE_CLUSTER_ADDRS",
	"CACHE_USERNAME",
	"CACHE_PASSWORD",
	"CACHE_DB",
	"CACHE_TLS_ENABLED",
}

// credentialKeys are passed through verbatim; every other value is trimmed so
// a trailing newline from kubectl create secret --from-file is tolerated.
var credentialKeys = []string{"CACHE_SENTINEL_USERNAME", "CACHE_SENTINEL_PASSWORD", "CACHE_USERNAME", "CACHE_PASSWORD"}

// Keys returns the session-store Secret keys exposed to the broker-router.
func Keys() []string {
	return slices.Clone(keys)
}

// modeKeys lists the keys each mode reads; any other set key is ignored.
var modeKeys = map[string][]string{
	"standalone": {"CACHE_CONNECTION_STRING"},
	"sentinel": {
		"CACHE_SENTINEL_MASTER", "CACHE_SENTINEL_ADDRS", "CACHE_SENTINEL_USERNAME", "CACHE_SENTINEL_PASSWORD",
		"CACHE_USERNAME", "CACHE_PASSWORD", "CACHE_DB", "CACHE_TLS_ENABLED",
	},
	"cluster": {"CACHE_CLUSTER_ADDRS", "CACHE_USERNAME", "CACHE_PASSWORD", "CACHE_DB", "CACHE_TLS_ENABLED"},
}

// Config holds validated connection settings. Construct it with Parse.
type Config struct {
	Mode string
	// Ignored lists set keys the selected mode does not read, so callers can
	// warn about likely misconfiguration. It holds key names only, never values.
	Ignored    []string
	standalone *redis.Options
	ha         redis.UniversalOptions
}

// Parse reads settings without connecting. Errors never include Secret values.
// An unset mode selects standalone; HA-only settings do not override a standalone URL.
func Parse(raw func(string) string) (*Config, error) {
	get := func(key string) string {
		if slices.Contains(credentialKeys, key) {
			return raw(key)
		}
		return strings.TrimSpace(raw(key))
	}
	cfg := &Config{Mode: strings.ToLower(get("CACHE_MODE"))}
	if cfg.Mode == "" {
		cfg.Mode = "standalone"
	}
	if used, ok := modeKeys[cfg.Mode]; ok {
		for _, key := range keys {
			if key != "CACHE_MODE" && get(key) != "" && !slices.Contains(used, key) {
				cfg.Ignored = append(cfg.Ignored, key)
			}
		}
	}
	if cfg.Mode == "standalone" {
		connectionString := get("CACHE_CONNECTION_STRING")
		if connectionString == "" {
			if len(cfg.Ignored) > 0 {
				return nil, errors.New("CACHE_CONNECTION_STRING is required for standalone mode; set CACHE_MODE to sentinel or cluster to use the other CACHE_* keys")
			}
			return nil, errors.New("CACHE_CONNECTION_STRING is required for standalone mode")
		}
		opts, err := redis.ParseURL(connectionString)
		if err != nil {
			// ParseURL errors can contain the full URL, including credentials
			return nil, errors.New("CACHE_CONNECTION_STRING is not a valid Redis URL")
		}
		cfg.standalone = opts
		return cfg, nil
	}

	var err error
	switch cfg.Mode {
	case "sentinel":
		cfg.ha.MasterName = get("CACHE_SENTINEL_MASTER")
		if cfg.ha.MasterName == "" {
			return nil, errors.New("CACHE_SENTINEL_MASTER is required for sentinel mode")
		}
		cfg.ha.Addrs, err = parseAddresses(get("CACHE_SENTINEL_ADDRS"), "CACHE_SENTINEL_ADDRS")
		cfg.ha.SentinelUsername = get("CACHE_SENTINEL_USERNAME")
		cfg.ha.SentinelPassword = get("CACHE_SENTINEL_PASSWORD")
	case "cluster":
		cfg.ha.Addrs, err = parseAddresses(get("CACHE_CLUSTER_ADDRS"), "CACHE_CLUSTER_ADDRS")
	default:
		return nil, errors.New("CACHE_MODE must be standalone, sentinel or cluster")
	}
	if err != nil {
		return nil, err
	}
	cfg.ha.Username = get("CACHE_USERNAME")
	cfg.ha.Password = get("CACHE_PASSWORD")
	if db := get("CACHE_DB"); db != "" {
		cfg.ha.DB, err = strconv.Atoi(db)
		if err != nil || cfg.ha.DB < 0 {
			return nil, errors.New("CACHE_DB must be a nonnegative integer")
		}
	}
	if cfg.Mode == "cluster" && cfg.ha.DB != 0 {
		return nil, errors.New("CACHE_DB must be 0 for cluster mode")
	}
	if enabled := get("CACHE_TLS_ENABLED"); enabled != "" {
		useTLS, err := strconv.ParseBool(enabled)
		if err != nil {
			return nil, errors.New("CACHE_TLS_ENABLED must be a boolean")
		}
		if useTLS {
			cfg.ha.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
	}
	return cfg, nil
}

func parseAddresses(value, key string) ([]string, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("%s is required", key)
	}
	addrs := strings.Split(value, ",")
	for i := range addrs {
		addrs[i] = strings.TrimSpace(addrs[i])
		host, port, err := net.SplitHostPort(addrs[i])
		if err != nil || host == "" || strings.ContainsAny(host, "@/\\?# \t\r\n") {
			return nil, fmt.Errorf("%s must contain comma-separated host:port addresses", key)
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("%s ports must be integers between 1 and 65535", key)
		}
	}
	return addrs, nil
}

// NewClient constructs a client from a parsed configuration; the caller owns its lifecycle.
func (c *Config) NewClient() redis.UniversalClient {
	switch c.Mode {
	case "sentinel":
		return redis.NewFailoverClient(c.ha.Failover())
	case "cluster":
		// explicit construction also supports clusters with a single seed address
		return redis.NewClusterClient(c.ha.Cluster())
	default:
		return redis.NewClient(c.standalone)
	}
}
