package mcprouter

import (
	"testing"

	basepb "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
)

func TestParseBaggage(t *testing.T) {
	tests := []struct {
		name          string
		baggageHeader string
		wantUser      string
		wantAgent     string
	}{
		{
			name:          "standard baggage",
			baggageHeader: "user.id=jane-doe,agent.id=helper-bot",
			wantUser:      "jane-doe",
			wantAgent:     "helper-bot",
		},
		{
			name:          "with properties",
			baggageHeader: "user.id=jane-doe;role=admin;env=prod,agent.id=helper-bot;version=1.0",
			wantUser:      "jane-doe",
			wantAgent:     "helper-bot",
		},
		{
			name:          "url-encoded with special characters",
			baggageHeader: "user.id=jane%20doe%21,agent.id=helper%2Fbot%3Fv%3D1",
			wantUser:      "jane doe!",
			wantAgent:     "helper/bot?v=1",
		},
		{
			name:          "header injection defense - strip CR/LF",
			baggageHeader: "user.id=jane%0Adoe%0Dnewline,agent.id=helper%00bot",
			wantUser:      "janedoenewline",
			wantAgent:     "helperbot",
		},
		{
			name:          "missing keys",
			baggageHeader: "somekey=someval,otherkey=otherval",
			wantUser:      "",
			wantAgent:     "",
		},
		{
			name:          "empty header",
			baggageHeader: "",
			wantUser:      "",
			wantAgent:     "",
		},
		{
			name:          "malformed baggage",
			baggageHeader: "user.id,agent.id=helper",
			wantUser:      "",
			wantAgent:     "helper",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotUser, gotAgent := ParseBaggage(tt.baggageHeader)
			if gotUser != tt.wantUser {
				t.Errorf("ParseBaggage() gotUser = %v, want %v", gotUser, tt.wantUser)
			}
			if gotAgent != tt.wantAgent {
				t.Errorf("ParseBaggage() gotAgent = %v, want %v", gotAgent, tt.wantAgent)
			}
		})
	}
}

func TestResolveCallerIdentity(t *testing.T) {
	identityHeaders := []string{"x-forwarded-email", "x-auth-user"}

	t.Run("use baggage user.id when present", func(t *testing.T) {
		headers := &basepb.HeaderMap{
			Headers: []*basepb.HeaderValue{
				{
					Key:      "x-forwarded-email",
					RawValue: []byte("fallback@example.com"),
				},
			},
		}
		baggage := "user.id=jane-doe,agent.id=helper-bot"
		user, agent := ResolveCallerIdentity(headers, baggage, identityHeaders)
		if user != "jane-doe" {
			t.Errorf("expected jane-doe, got %v", user)
		}
		if agent != "helper-bot" {
			t.Errorf("expected helper-bot, got %v", agent)
		}
	})

	t.Run("fallback to first identity header", func(t *testing.T) {
		headers := &basepb.HeaderMap{
			Headers: []*basepb.HeaderValue{
				{
					Key:      "x-forwarded-email",
					RawValue: []byte("email@example.com"),
				},
				{
					Key:      "x-auth-user",
					RawValue: []byte("authuser"),
				},
			},
		}
		user, agent := ResolveCallerIdentity(headers, "", identityHeaders)
		if user != "email@example.com" {
			t.Errorf("expected email@example.com, got %v", user)
		}
		if agent != "" {
			t.Errorf("expected empty agent, got %v", agent)
		}
	})

	t.Run("fallback to second identity header when first absent", func(t *testing.T) {
		headers := &basepb.HeaderMap{
			Headers: []*basepb.HeaderValue{
				{
					Key:      "x-auth-user",
					RawValue: []byte("authuser"),
				},
			},
		}
		user, _ := ResolveCallerIdentity(headers, "", identityHeaders)
		if user != "authuser" {
			t.Errorf("expected authuser, got %v", user)
		}
	})

	t.Run("no identity found", func(t *testing.T) {
		headers := &basepb.HeaderMap{}
		user, _ := ResolveCallerIdentity(headers, "", identityHeaders)
		if user != "" {
			t.Errorf("expected empty user, got %v", user)
		}
	})
}
