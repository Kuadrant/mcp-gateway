package broker

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetOAuthConfig(t *testing.T) {
	// The test shouldn't be run with env vars set.
	// We could use t.Setenv() to make test cases, but then the test couldn't run in parallel.
	require.Equal(t, "", os.Getenv(envOAuthResourceName), "Test case expects env var to be unset")
	require.Equal(t, "", os.Getenv(envOAuthResource), "Test case expects env var to be unset")
	require.Equal(t, "", os.Getenv(envOAuthAuthorizationServers), "Test case expects env var to be unset")
	require.Equal(t, "", os.Getenv(envOAuthBearerMethodsSupported), "Test case expects env var to be unset")
	require.Equal(t, "", os.Getenv(envOAuthScopesSupported), "Test case expects env var to be unset")

	result := getOAuthConfig()
	require.NotNil(t, result)
	require.Equal(t, "MCP Server", result.ResourceName)
	require.Equal(t, "/mcp", result.Resource)
	require.Equal(t, []string{}, result.AuthorizationServers)
	require.Equal(t, []string{"header"}, result.BearerMethodsSupported)
	require.Equal(t, []string{"basic"}, result.ScopesSupported)
}

func TestProtectedResourceHandler_Handle(t *testing.T) {
	// The test shouldn't be run with env vars set.
	// We could use t.Setenv() to make test cases, but then the test couldn't run in parallel.
	require.Equal(t, "", os.Getenv(envOAuthResourceName), "Test case expects env var to be unset")
	require.Equal(t, "", os.Getenv(envOAuthResource), "Test case expects env var to be unset")
	require.Equal(t, "", os.Getenv(envOAuthAuthorizationServers), "Test case expects env var to be unset")
	require.Equal(t, "", os.Getenv(envOAuthBearerMethodsSupported), "Test case expects env var to be unset")
	require.Equal(t, "", os.Getenv(envOAuthScopesSupported), "Test case expects env var to be unset")

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	testCases := []struct {
		name           string
		method         string
		expectedStatus int
		checkBody      bool
	}{
		{
			name:           "GET request returns JSON response",
			method:         http.MethodGet,
			expectedStatus: http.StatusOK,
			checkBody:      true,
		},
		{
			name:           "POST request returns JSON response",
			method:         http.MethodPost,
			expectedStatus: http.StatusOK,
			checkBody:      true,
		},
		{
			name:           "OPTIONS preflight request returns 200",
			method:         http.MethodOptions,
			expectedStatus: http.StatusOK,
			checkBody:      false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			handler := &ProtectedResourceHandler{Logger: logger}

			req := httptest.NewRequest(tc.method, "/.well-known/oauth-protected-resource", nil)
			rec := httptest.NewRecorder()

			handler.Handle(rec, req)

			require.Equal(t, tc.expectedStatus, rec.Code)

			// check CORS headers
			require.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
			require.Contains(t, rec.Header().Get("Access-Control-Allow-Methods"), "GET")
			require.Contains(t, rec.Header().Get("Access-Control-Allow-Headers"), "Authorization")

			if tc.checkBody {
				require.Equal(t, "application/json", rec.Header().Get("Content-Type"))

				var response OAuthProtectedResource
				err := json.NewDecoder(rec.Body).Decode(&response)
				require.NoError(t, err)

				// verify default values
				require.Equal(t, "MCP Server", response.ResourceName)
				require.Equal(t, "/mcp", response.Resource)
			}
		})
	}
}
