package config_test

import (
	"errors"
	"net/url"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
)

func TestConfig_MCPServerPath(t *testing.T) {
	testCases := []struct {
		Name   string
		Server *config.MCPServer
		Error  error
		Out    string
	}{
		{
			Name: "test get mcp server path when set",
			Server: &config.MCPServer{
				URL: "http://mcp-api-key-server.mcp-test.svc.cluster.local:9090/mcp",
			},
			Error: nil,
			Out:   "/mcp",
		},
		{
			Name: "test get mcp server path when set",
			Server: &config.MCPServer{
				URL: "http://mcp-api-key-server.mcp-test.svc.cluster.local:-9090/mcp",
			},
			Error: &url.Error{},
			Out:   "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			path, err := tc.Server.Path()
			if err != nil {
				if tc.Error != nil && errors.Is(err, tc.Error) {
					t.Fatalf("expected err %v but got %v", tc.Error, err)
				}
				if tc.Error == nil {
					t.Fatalf("did not expect an error but got %v", err)
				}
			}
			if path != tc.Out {
				t.Fatalf("expected path to be %s but got %s", tc.Out, path)
			}

		})
	}
}
