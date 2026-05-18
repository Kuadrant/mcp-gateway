package mcprouter

import (
	"net/url"
	"strings"

	basepb "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
)

// ParseBaggage parses a W3C baggage header value and returns user.id and agent.id if present.
func ParseBaggage(baggageHeader string) (string, string) {
	if baggageHeader == "" {
		return "", ""
	}
	var userID, agentID string
	// Split by comma
	items := strings.Split(baggageHeader, ",")
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		// Each item can have properties separated by semicolon, e.g. key=val;prop=val2
		parts := strings.Split(item, ";")
		keyVal := strings.TrimSpace(parts[0])
		if keyVal == "" {
			continue
		}
		eqIdx := strings.Index(keyVal, "=")
		if eqIdx == -1 {
			continue
		}
		key := strings.TrimSpace(keyVal[:eqIdx])
		val := strings.TrimSpace(keyVal[eqIdx+1:])
		if key == "user.id" {
			userID = sanitizeBaggageValue(val)
		} else if key == "agent.id" {
			agentID = sanitizeBaggageValue(val)
		}
	}
	return userID, agentID
}

func sanitizeBaggageValue(val string) string {
	decoded, err := url.QueryUnescape(val)
	if err != nil {
		decoded = val
	}
	// Strip control characters: CR (\r), LF (\n), NULL (\x00)
	r := strings.NewReplacer("\r", "", "\n", "", "\x00", "")
	return r.Replace(decoded)
}

// ResolveCallerIdentity resolves the caller's user and agent identity.
// It checks the baggage user.id and agent.id first.
// If user.id is absent, it checks the identityHeaders in order.
func ResolveCallerIdentity(headers *basepb.HeaderMap, baggageHeader string, identityHeaders []string) (string, string) {
	userID, agentID := ParseBaggage(baggageHeader)
	if userID != "" {
		return userID, agentID
	}
	// Try fallback headers in order
	for _, hName := range identityHeaders {
		hName = strings.TrimSpace(strings.ToLower(hName))
		if val := getSingleValueHeader(headers, hName); val != "" {
			return sanitizeBaggageValue(val), agentID
		}
	}
	return "", agentID
}
