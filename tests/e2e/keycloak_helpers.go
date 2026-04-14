//go:build e2e

package e2e

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	goenv "github.com/caitlinelfring/go-env-default"
)

var keycloakTokenURL = goenv.GetDefault("KEYCLOAK_TOKEN_URL", "https://keycloak."+e2eDomain+":8002/realms/mcp/protocol/openid-connect/token")

// obtains an access token via ROPC grant (test automation only)
func GetKeycloakUserToken(username, password string) (string, error) {
	data := url.Values{
		"grant_type":    {"password"},
		"client_id":     {"mcp-gateway"},
		"client_secret": {"secret"},
		"username":      {username},
		"password":      {password},
		"scope":         {"openid groups roles"},
	}

	req, err := http.NewRequest("POST", keycloakTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // self-signed cert in Kind
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token request returned %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return "", fmt.Errorf("no access_token in response")
	}
	return tokenResp.AccessToken, nil
}
