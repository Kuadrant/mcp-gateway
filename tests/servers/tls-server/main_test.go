package main

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

const (
	testClientID     = "e2e-client"
	testClientSecret = "e2e-secret"
)

// newFixture serves the two halves on separate listeners, as the deployment does.
func newFixture(t *testing.T) (store *clientStore, tokenURL, mcpURL string) {
	t.Helper()
	store = newClientStore(map[string]string{testClientID: testClientSecret})

	tokenMux := http.NewServeMux()
	tokenMux.HandleFunc("POST /token", store.handleToken)
	as := httptest.NewServer(tokenMux)
	t.Cleanup(as.Close)

	upstream := httptest.NewServer(store.protectedHandler())
	t.Cleanup(upstream.Close)

	return store, as.URL + "/token", upstream.URL + "/mcp"
}

func connect(ctx context.Context, endpoint string, hc *http.Client) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	return client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           hc,
		DisableStandaloneSSE: true,
	}, nil)
}

func TestParseClients(t *testing.T) {
	for name, tc := range map[string]struct {
		spec    string
		want    map[string]string
		wantErr bool
	}{
		"single":       {spec: "a:b", want: map[string]string{"a": "b"}},
		"multiple":     {spec: "a:b,c:d", want: map[string]string{"a": "b", "c": "d"}},
		"missing pair": {spec: "ab", wantErr: true},
		"empty secret": {spec: "a:", wantErr: true},
		"empty id":     {spec: ":b", wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseClients(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseClients(%q) = %v, want error", tc.spec, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseClients(%q): %v", tc.spec, err)
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("parseClients(%q) = %v, want %v", tc.spec, got, tc.want)
			}
		})
	}
}

// x/oauth2 probes client_secret_basic then client_secret_post, so both have to work.
func TestTokenEndpoint_AcceptsBothAuthStyles(t *testing.T) {
	for name, style := range map[string]oauth2.AuthStyle{
		"client_secret_basic": oauth2.AuthStyleInHeader,
		"client_secret_post":  oauth2.AuthStyleInParams,
	} {
		t.Run(name, func(t *testing.T) {
			store, tokenURL, _ := newFixture(t)
			cfg := &clientcredentials.Config{
				ClientID:     testClientID,
				ClientSecret: testClientSecret,
				TokenURL:     tokenURL,
				Scopes:       []string{"mcp.read"},
				AuthStyle:    style,
			}
			tok, err := cfg.Token(context.Background())
			if err != nil {
				t.Fatalf("Token(): %v", err)
			}
			if tok.TokenType != "Bearer" {
				t.Errorf("token_type = %q, want Bearer", tok.TokenType)
			}
			if tok.Expiry.IsZero() {
				t.Error("token has no expiry, want expires_in in the response")
			}
			if id, ok := store.clientFor(tok.AccessToken); !ok || id != testClientID {
				t.Errorf("token bound to %q (found=%v), want %q", id, ok, testClientID)
			}
		})
	}
}

func TestTokenEndpoint_RejectsBadCredentials(t *testing.T) {
	_, tokenURL, _ := newFixture(t)
	for name, form := range map[string]url.Values{
		"wrong secret": {
			"grant_type":    {"client_credentials"},
			"client_id":     {testClientID},
			"client_secret": {"wrong"},
		},
		"unknown client": {
			"grant_type":    {"client_credentials"},
			"client_id":     {"nobody"},
			"client_secret": {testClientSecret},
		},
		"unsupported grant": {
			"grant_type":    {"password"},
			"client_id":     {testClientID},
			"client_secret": {testClientSecret},
		},
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := http.PostForm(tokenURL, form)
			if err != nil {
				t.Fatalf("PostForm: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", resp.StatusCode)
			}
			var body map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["error"] != "invalid_client" {
				t.Errorf("body = %v, want error=invalid_client", body)
			}
		})
	}
}

func TestProtectedMCP_RequiresIssuedToken(t *testing.T) {
	_, tokenURL, mcpURL := newFixture(t)
	ctx := context.Background()

	t.Run("no token", func(t *testing.T) {
		if _, err := connect(ctx, mcpURL, http.DefaultClient); err == nil {
			t.Fatal("connect succeeded without a token, want failure")
		}
	})

	t.Run("token not issued here", func(t *testing.T) {
		hc := &http.Client{Transport: &oauth2.Transport{
			Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "forged", TokenType: "Bearer"}),
		}}
		if _, err := connect(ctx, mcpURL, hc); err == nil {
			t.Fatal("connect succeeded with a forged token, want failure")
		}
	})

	t.Run("minted token", func(t *testing.T) {
		cfg := &clientcredentials.Config{
			ClientID:     testClientID,
			ClientSecret: testClientSecret,
			TokenURL:     tokenURL,
		}
		session, err := connect(ctx, mcpURL, cfg.Client(ctx))
		if err != nil {
			t.Fatalf("connect with minted token: %v", err)
		}
		defer session.Close()

		tools, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("ListTools: %v", err)
		}
		var whoami *mcp.Tool
		for _, tool := range tools.Tools {
			if tool.Name == "whoami" {
				whoami = tool
			}
		}
		if whoami == nil {
			t.Fatalf("tools = %v, want whoami", tools.Tools)
		}
		// the description is the only place the minted identity is observable
		// from outside: a client's tools/call never carries the broker's token
		if !strings.Contains(whoami.Description, testClientID) {
			t.Errorf("whoami description = %q, want it to name %q", whoami.Description, testClientID)
		}
	})
}
