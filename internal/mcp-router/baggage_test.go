package mcprouter

import (
	"testing"
)

func TestParseBaggage(t *testing.T) {
	tests := []struct {
		name      string
		baggage   string
		wantUser  string
		wantAgent string
	}{
		{
			name:      "both user.id and agent.id present",
			baggage:   "user.id=jane,agent.id=coding-agent-v2",
			wantUser:  "jane",
			wantAgent: "coding-agent-v2",
		},
		{
			name:      "only user.id",
			baggage:   "user.id=jane,other=value",
			wantUser:  "jane",
			wantAgent: "",
		},
		{
			name:      "only agent.id",
			baggage:   "agent.id=my-agent",
			wantUser:  "",
			wantAgent: "my-agent",
		},
		{
			name:      "empty baggage",
			baggage:   "",
			wantUser:  "",
			wantAgent: "",
		},
		{
			name:      "url-encoded values",
			baggage:   "user.id=jane%40example.com,agent.id=agent%2Fv2",
			wantUser:  "jane@example.com",
			wantAgent: "agent/v2",
		},
		{
			name:      "values with spaces encoded",
			baggage:   "user.id=Jane%20Doe,agent.id=my%20agent",
			wantUser:  "Jane Doe",
			wantAgent: "my agent",
		},
		{
			name:      "strips CR from decoded value",
			baggage:   "user.id=ab%0Dcd",
			wantUser:  "abcd",
			wantAgent: "",
		},
		{
			name:      "strips LF from decoded value",
			baggage:   "user.id=ab%0Acd",
			wantUser:  "abcd",
			wantAgent: "",
		},
		{
			name:      "strips CRLF from decoded value",
			baggage:   "user.id=ab%0D%0Acd",
			wantUser:  "abcd",
			wantAgent: "",
		},
		{
			name:      "strips null bytes from decoded value",
			baggage:   "agent.id=ab%00cd",
			wantUser:  "",
			wantAgent: "abcd",
		},
		{
			name:      "malformed percent encoding treated gracefully",
			baggage:   "user.id=%GG,agent.id=ok",
			wantUser:  "%GG",
			wantAgent: "ok",
		},
		{
			name:      "whitespace around values trimmed per W3C spec",
			baggage:   "user.id= jane , agent.id= bot ",
			wantUser:  "jane",
			wantAgent: "bot",
		},
		{
			name:      "semicolon-separated properties ignored",
			baggage:   "user.id=jane;property=val,agent.id=bot",
			wantUser:  "jane",
			wantAgent: "bot",
		},
		{
			name:      "missing keys in baggage",
			baggage:   "other.key=value,another=thing",
			wantUser:  "",
			wantAgent: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotUser, gotAgent := parseBaggage(tt.baggage)
			if gotUser != tt.wantUser {
				t.Errorf("parseBaggage() user = %q, want %q", gotUser, tt.wantUser)
			}
			if gotAgent != tt.wantAgent {
				t.Errorf("parseBaggage() agent = %q, want %q", gotAgent, tt.wantAgent)
			}
		})
	}
}

func TestResolveUserID(t *testing.T) {
	tests := []struct {
		name            string
		baggageUserID   string
		headers         map[string]string
		identityHeaders []string
		want            string
	}{
		{
			name:          "baggage user.id takes precedence",
			baggageUserID: "jane",
			headers:       map[string]string{"x-forwarded-email": "other@example.com"},
			want:          "jane",
		},
		{
			name:            "falls back to first matching identity header",
			baggageUserID:   "",
			headers:         map[string]string{"x-forwarded-email": "jane@example.com", "x-auth-user": "also-jane"},
			identityHeaders: []string{"x-forwarded-email", "x-auth-user"},
			want:            "jane@example.com",
		},
		{
			name:            "skips empty headers in fallback chain",
			baggageUserID:   "",
			headers:         map[string]string{"x-forwarded-email": "", "x-auth-user": "jane"},
			identityHeaders: []string{"x-forwarded-email", "x-auth-user"},
			want:            "jane",
		},
		{
			name:            "returns empty when no fallback matches",
			baggageUserID:   "",
			headers:         map[string]string{},
			identityHeaders: []string{"x-forwarded-email", "x-auth-user"},
			want:            "",
		},
		{
			name:            "nil identity headers uses defaults",
			baggageUserID:   "",
			headers:         map[string]string{"x-forwarded-email": "from-default"},
			identityHeaders: nil,
			want:            "from-default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headerFn := func(name string) string {
				return tt.headers[name]
			}
			identityHeaders := tt.identityHeaders
			if identityHeaders == nil {
				identityHeaders = defaultIdentityHeaders
			}
			got := resolveUserID(tt.baggageUserID, headerFn, identityHeaders)
			if got != tt.want {
				t.Errorf("resolveUserID() = %q, want %q", got, tt.want)
			}
		})
	}
}
