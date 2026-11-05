package redisconfig

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/stretchr/testify/require"
)

func TestStandaloneURLRemainsAuthoritative(t *testing.T) {
	r := miniredis.RunT(t)
	r.RequireAuth("url-password")
	settings := map[string]string{
		"CACHE_CONNECTION_STRING": "redis://default:url-password@" + r.Addr() + "/2?max_retries=1",
		"CACHE_PASSWORD":          "ignored-ha-password",
		"CACHE_DB":                "invalid-unused-ha-setting",
		"CACHE_TLS_ENABLED":       "invalid-unused-ha-setting",
	}
	cfg, err := Parse(func(key string) string { return settings[key] })
	require.NoError(t, err)
	client := cfg.NewClient()
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	require.NoError(t, client.Set(t.Context(), "session", "backend-session", time.Minute).Err())
	value, err := r.DB(2).Get("session")
	require.NoError(t, err)
	require.Equal(t, "backend-session", value)
	require.False(t, r.DB(0).Exists("session"))
}

func TestParseReportsIgnoredKeys(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]string
		want     []string
	}{
		{
			name:     "standalone ignores HA keys",
			settings: map[string]string{"CACHE_CONNECTION_STRING": "redis://localhost:6379", "CACHE_PASSWORD": "x", "CACHE_TLS_ENABLED": "true"},
			want:     []string{"CACHE_PASSWORD", "CACHE_TLS_ENABLED"},
		},
		{
			name: "sentinel ignores URL and cluster keys",
			settings: map[string]string{
				"CACHE_MODE": "sentinel", "CACHE_SENTINEL_MASTER": "mymaster", "CACHE_SENTINEL_ADDRS": "s:26379",
				"CACHE_CONNECTION_STRING": "redis://localhost:6379", "CACHE_CLUSTER_ADDRS": "c:6379",
			},
			want: []string{"CACHE_CONNECTION_STRING", "CACHE_CLUSTER_ADDRS"},
		},
		{
			name:     "cluster ignores sentinel keys",
			settings: map[string]string{"CACHE_MODE": "cluster", "CACHE_CLUSTER_ADDRS": "c:6379", "CACHE_SENTINEL_MASTER": "mymaster"},
			want:     []string{"CACHE_SENTINEL_MASTER"},
		},
		{
			name:     "no ignored keys",
			settings: map[string]string{"CACHE_MODE": "cluster", "CACHE_CLUSTER_ADDRS": "c:6379", "CACHE_PASSWORD": "x"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse(func(key string) string { return tc.settings[key] })
			require.NoError(t, err)
			require.Equal(t, tc.want, cfg.Ignored)
		})
	}
}

func TestParseNormalizesValues(t *testing.T) {
	// kubectl create secret --from-file keeps the trailing newline
	settings := map[string]string{
		"CACHE_MODE":            "Sentinel\n",
		"CACHE_SENTINEL_MASTER": "mymaster\n",
		"CACHE_SENTINEL_ADDRS":  "s-0:26379, s-1:26379\n",
		"CACHE_DB":              "2\n",
		"CACHE_TLS_ENABLED":     "true\n",
		"CACHE_PASSWORD":        " keep spaces ",
	}
	cfg, err := Parse(func(key string) string { return settings[key] })
	require.NoError(t, err)
	require.Equal(t, "sentinel", cfg.Mode)
	require.Equal(t, "mymaster", cfg.ha.MasterName)
	require.Equal(t, []string{"s-0:26379", "s-1:26379"}, cfg.ha.Addrs)
	require.Equal(t, 2, cfg.ha.DB)
	require.NotNil(t, cfg.ha.TLSConfig)
	require.Equal(t, " keep spaces ", cfg.ha.Password, "credentials are passed through verbatim")
}

func TestParseMissingModeWithHAKeys(t *testing.T) {
	settings := map[string]string{"CACHE_SENTINEL_MASTER": "mymaster", "CACHE_SENTINEL_ADDRS": "s-0:26379"}
	_, err := Parse(func(key string) string { return settings[key] })
	require.ErrorContains(t, err, "set CACHE_MODE to sentinel or cluster")
}

func TestKeysReturnsCopy(t *testing.T) {
	k := Keys()
	k[0] = "MUTATED"
	require.Equal(t, "CACHE_MODE", Keys()[0])
}

func TestClusterSingleSeedDiscoversDataNode(t *testing.T) {
	seed := miniredis.RunT(t)
	data := miniredis.RunT(t)
	seed.RequireUserAuth("gateway", "cluster-password")
	data.RequireUserAuth("gateway", "cluster-password")
	port, err := strconv.Atoi(data.Port())
	require.NoError(t, err)
	seed.Server().SetPreHook(func(peer *server.Peer, cmd string, args ...string) bool {
		if cmd != "CLUSTER" || len(args) != 1 || args[0] != "slots" {
			return false
		}
		peer.WriteLen(1)
		peer.WriteLen(3)
		peer.WriteInt(0)
		peer.WriteInt(16383)
		peer.WriteLen(2)
		peer.WriteBulk(data.Host())
		peer.WriteInt(port)
		return true
	})
	settings := map[string]string{
		"CACHE_MODE":          "cluster",
		"CACHE_CLUSTER_ADDRS": " " + seed.Addr() + " ",
		"CACHE_USERNAME":      "gateway",
		"CACHE_PASSWORD":      "cluster-password",
	}
	cfg, err := Parse(func(key string) string { return settings[key] })
	require.NoError(t, err)
	client := cfg.NewClient()
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	require.NoError(t, client.Set(t.Context(), "session", "backend-session", time.Minute).Err())
	value, err := data.Get("session")
	require.NoError(t, err)
	require.Equal(t, "backend-session", value)
	require.False(t, seed.Exists("session"))
	value, err = client.Get(t.Context(), "session").Result()
	require.NoError(t, err)
	require.Equal(t, "backend-session", value)
}

func TestClusterTLSRejectsUntrustedCertificate(t *testing.T) {
	certServer := httptest.NewTLSServer(http.NotFoundHandler())
	tlsConfig := certServer.TLS.Clone()
	certServer.Close()
	r := miniredis.NewMiniRedis()
	require.NoError(t, r.StartTLS(tlsConfig))
	t.Cleanup(r.Close)
	settings := map[string]string{
		"CACHE_MODE":          "cluster",
		"CACHE_CLUSTER_ADDRS": r.Addr(),
		"CACHE_TLS_ENABLED":   "true",
	}
	cfg, err := Parse(func(key string) string { return settings[key] })
	require.NoError(t, err)
	client := cfg.NewClient()
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var verificationError *tls.CertificateVerificationError
	require.ErrorAs(t, client.Ping(ctx).Err(), &verificationError)
}
