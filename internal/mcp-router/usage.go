package mcprouter

import (
	"sync"
	"sync/atomic"

	"github.com/Kuadrant/mcp-gateway/internal/protocol"
	"github.com/Kuadrant/mcp-gateway/internal/routing"
)

// usedVersions maps server name to a bitmask over protocol.ClientVersions.
// copy-on-write: the hot path only loads; a write happens once per new
// server and version pair.
var (
	usedVersions   atomic.Pointer[map[string]uint8]
	usedVersionsMu sync.Mutex
)

func init() {
	usedVersions.Store(&map[string]uint8{})
}

// clientProtocolVersion returns the version a client is speaking: the
// MCP-Protocol-Version header, or params.protocolVersion on a 2025 initialize
// which is sent before the header is negotiated. no version means the default.
func clientProtocolVersion(header string, req *routing.MCPRequest) string {
	if header != "" {
		return header
	}
	if req != nil && req.Method == "initialize" {
		if v, ok := req.Params["protocolVersion"].(string); ok && v != "" {
			return v
		}
	}
	return protocol.DefaultClientVersion
}

func versionBit(v string) uint8 {
	for i, known := range protocol.ClientVersions {
		if v == known {
			return 1 << i
		}
	}
	return 0
}

// recordServerUse notes that a client used its protocol version with server.
// hairpin initializes sent by the router itself carry mcp-init-host and are skipped.
func recordServerUse(server, header, initHost string, req *routing.MCPRequest) {
	if server == "" || initHost != "" {
		return
	}
	bit := versionBit(clientProtocolVersion(header, req))
	if bit == 0 || (*usedVersions.Load())[server]&bit != 0 {
		return
	}
	usedVersionsMu.Lock()
	defer usedVersionsMu.Unlock()
	cur := *usedVersions.Load()
	if cur[server]&bit != 0 {
		return
	}
	next := make(map[string]uint8, len(cur)+1)
	for k, v := range cur {
		next[k] = v
	}
	next[server] |= bit
	usedVersions.Store(&next)
}

// UsedProtocolVersions returns the protocol versions clients used with each
// upstream server since process start, keyed by server name (namespace/name).
func UsedProtocolVersions() map[string][]string {
	cur := *usedVersions.Load()
	out := make(map[string][]string, len(cur))
	for server, bits := range cur {
		for i, v := range protocol.ClientVersions {
			if bits&(1<<i) != 0 {
				out[server] = append(out[server], v)
			}
		}
	}
	return out
}
