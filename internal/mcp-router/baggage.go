package mcprouter

import (
	"net/url"
	"strings"
)

var defaultIdentityHeaders = []string{"x-forwarded-email", "x-auth-user"}

// parseBaggage extracts user.id and agent.id from a W3C Baggage header value.
// Values are URL-decoded and control characters (CR, LF, null) are stripped.
func parseBaggage(header string) (userID, agentID string) {
	if header == "" {
		return "", ""
	}
	for _, member := range strings.Split(header, ",") {
		member = strings.TrimSpace(member)
		if member == "" {
			continue
		}
		// strip properties (;key=val) per W3C Baggage spec
		if idx := strings.IndexByte(member, ';'); idx >= 0 {
			member = member[:idx]
		}
		key, val, ok := strings.Cut(member, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		decoded, err := url.QueryUnescape(val)
		if err != nil {
			decoded = val
		}
		decoded = stripControlChars(decoded)
		switch key {
		case "user.id":
			userID = decoded
		case "agent.id":
			agentID = decoded
		}
	}
	return userID, agentID
}

// stripControlChars removes CR, LF, and null bytes to prevent header injection.
func stripControlChars(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return -1
		}
		return r
	}, s)
}

// resolveUserID returns the caller identity: baggage user.id if present,
// otherwise the first non-empty value from the identity header fallback chain.
func resolveUserID(baggageUserID string, getHeader func(string) string, identityHeaders []string) string {
	if baggageUserID != "" {
		return baggageUserID
	}
	for _, h := range identityHeaders {
		if v := getHeader(h); v != "" {
			return v
		}
	}
	return ""
}
