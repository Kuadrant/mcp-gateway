// main measures synthetic broker configs using the controller's YAML serializer.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

type profile struct {
	name           string
	credential     string
	gatewayCA      string
	serverCA       string
	virtualTools   int
	virtualPrompts int
}

func main() {
	cert := certificate()
	bundle := strings.Repeat(cert, 8)
	credential := strings.Repeat("x", 1024)
	profiles := []profile{
		{name: "lean"},
		{name: "credential", credential: credential},
		{name: "shared-ca", credential: credential, gatewayCA: bundle},
		{name: "per-server-ca", credential: credential, serverCA: bundle},
		{name: "large-server-ca", credential: credential, serverCA: strings.Repeat(cert, (64*1024)/len(cert))},
		{name: "large-shared-ca", credential: credential, gatewayCA: strings.Repeat(cert, (256*1024)/len(cert))},
		{name: "virtuals", credential: credential, virtualTools: 100, virtualPrompts: 10},
	}
	fmt.Printf("Secret data limit: %d bytes\n", corev1.MaxSecretSize)
	fmt.Printf("Certificate: %d bytes; eight-certificate bundle: %d bytes\n", len(cert), len(bundle))
	fmt.Printf("Large server bundle: %d bytes; large shared bundle: %d bytes\n\n", len(profiles[4].serverCA), len(profiles[5].gatewayCA))
	fmt.Println("profile,servers,virtual_servers,config_bytes,percent_of_limit")
	for _, p := range profiles {
		for _, count := range []int{10, 100, 500, 1000} {
			cfg := configuration(p, count)
			printSize(p.name, count, len(cfg.VirtualServers), size(cfg))
		}
	}
	fmt.Println("\nprofile,first_server_count_over_limit,previous_bytes,over_limit_bytes")
	for _, p := range profiles {
		count := firstOverLimit(func(n int) int { return size(configuration(p, n)) })
		fmt.Printf("%s,%d,%d,%d\n", p.name, count, size(configuration(p, count-1)), size(configuration(p, count)))
	}
	fmt.Println("\nFixed 100 credential-bearing servers; vary only virtual-server count:")
	fmt.Println("profile,servers,virtual_servers,config_bytes,percent_of_limit")
	base := configuration(profiles[1], 100)
	for _, count := range []int{0, 10, 100, 500, 1000} {
		base.VirtualServers = virtualServers(count, 100, 100, 10)
		printSize("virtual-only-growth", 100, count, size(base))
	}
	count := firstOverLimit(func(n int) int {
		base.VirtualServers = virtualServers(n, 100, 100, 10)
		return size(base)
	})
	fmt.Printf("First virtual-server count over limit with 100 servers: %d\n", count)
}

func configuration(p profile, count int) config.BrokerConfig {
	cfg := config.BrokerConfig{GatewayCACertPEM: p.gatewayCA, MaxBodyBytes: config.DefaultMaxBodyBytes}
	scheme := "http"
	if p.serverCA != "" {
		scheme = "https"
	}
	for i := range count {
		cfg.Servers = append(cfg.Servers, config.MCPServer{
			Name:       fmt.Sprintf("team/server-%05d", i),
			URL:        fmt.Sprintf("%s://server-%05d.team.svc.cluster.local:8080/mcp", scheme, i),
			Hostname:   fmt.Sprintf("server-%05d.example.test", i),
			Prefix:     fmt.Sprintf("server_%05d", i),
			State:      "Enabled",
			Category:   []string{"uncategorised"},
			Credential: p.credential,
			CACert:     p.serverCA,
		})
	}
	if p.virtualTools > 0 {
		cfg.VirtualServers = virtualServers(count, count, p.virtualTools, p.virtualPrompts)
	}
	return cfg
}

func virtualServers(count, servers, tools, prompts int) []config.VirtualServerConfig {
	var result []config.VirtualServerConfig
	for i := range count {
		virtual := config.VirtualServerConfig{Name: fmt.Sprintf("team/virtual-%05d", i)}
		for j := range tools {
			virtual.Tools = append(virtual.Tools, fmt.Sprintf("server_%05d_tool_%03d", i%servers, j))
		}
		for j := range prompts {
			virtual.Prompts = append(virtual.Prompts, fmt.Sprintf("server_%05d_prompt_%03d", i%servers, j))
		}
		result = append(result, virtual)
	}
	return result
}

func size(cfg config.BrokerConfig) int {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	return len(data)
}

func printSize(name string, servers, virtuals, bytes int) {
	fmt.Printf("%s,%d,%d,%d,%.2f\n", name, servers, virtuals, bytes, 100*float64(bytes)/float64(corev1.MaxSecretSize))
}

func firstOverLimit(measure func(int) int) int {
	low, high := 0, 1
	for measure(high) <= corev1.MaxSecretSize {
		low, high = high, high*2
		if high > 65536 {
			panic("no boundary found within 65536 entries")
		}
	}
	for low+1 < high {
		mid := low + (high-low)/2
		if measure(mid) > corev1.MaxSecretSize {
			high = mid
		} else {
			low = mid
		}
	}
	return high
}

func certificate() string {
	// fixed synthetic key and dates make byte counts reproducible; not used for authentication
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "config-size.example.test"},
		NotBefore:             time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
