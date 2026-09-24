package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	extProcV3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestSetupLoggerLevelMapping(t *testing.T) {
	cases := []struct {
		name  string
		level int
		want  slog.Level
	}{
		{"info", 0, slog.LevelInfo},
		{"warn", 4, slog.LevelWarn},
		{"error", 8, slog.LevelError},
		{"debug", -4, slog.LevelDebug},
		{"arbitrary", 2, slog.Level(2)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &app{}
			a.brokerCfg.logLevel = tc.level
			opts, _ := a.setupLogger()
			if got := opts.Level.Level(); got != tc.want {
				t.Errorf("log-level=%d: got %v, want %v", tc.level, got, tc.want)
			}
		})
	}
}

// TestApplyConfigSnapshot tests the applyConfigSnapshot function.
func TestApplyConfigSnapshot(t *testing.T) {
	a := &app{
		mcpConfig: &config.MCPServersConfig{},
		logger:    slog.New(slog.NewTextHandler(os.Stdout, nil)),
	}

	t.Run("nil global guardrails stores nil checker", func(t *testing.T) {
		if err := a.applyConfigSnapshot(context.Background(), &configSnapshot{}); err != nil {
			t.Fatal(err)
		}
		if a.mcpConfig.GetGuardrailsChecker() != nil {
			t.Fatal("expected nil checker")
		}
	})

	t.Run("global guardrails creates a checker", func(t *testing.T) {
		if err := a.applyConfigSnapshot(context.Background(), &configSnapshot{
			globalGuardrails: &config.GuardrailsConfig{
				URL:   "http://127.0.0.1:1",
				Model: "test-model",
			},
		}); err != nil {
			t.Fatal(err)
		}
		if a.mcpConfig.GetGuardrailsChecker() == nil {
			t.Fatal("expected checker to be created")
		}
	})

	t.Run("clearing global guardrails destroys the checker", func(t *testing.T) {
		if err := a.applyConfigSnapshot(context.Background(), &configSnapshot{}); err != nil {
			t.Fatal(err)
		}
		if a.mcpConfig.GetGuardrailsChecker() != nil {
			t.Fatal("expected nil checker after clearing guardrails")
		}
	})

	t.Run("invalid gateway CA aborts without swapping live config", func(t *testing.T) {
		prevServers := []*config.MCPServer{{Name: "prev"}}
		a.mcpConfig.ApplyReload(prevServers, nil, "prev-ca", 0, nil, nil)

		err := a.applyConfigSnapshot(context.Background(), &configSnapshot{
			servers:          []*config.MCPServer{{Name: "next"}},
			gatewayCACertPEM: "not a certificate",
			globalGuardrails: &config.GuardrailsConfig{
				URL:   "http://127.0.0.1:1",
				Model: "test-model",
			},
		})
		if err == nil {
			t.Fatal("expected error for invalid CA PEM")
		}
		servers := a.mcpConfig.ListServers()
		if len(servers) != 1 || servers[0].Name != "prev" {
			t.Fatalf("live servers = %+v, want previous snapshot", servers)
		}
		if a.mcpConfig.GetGuardrailsChecker() != nil {
			t.Fatal("checker must stay nil when reload aborts")
		}
		if a.mcpConfig.GetGlobalGuardrails() != nil {
			t.Fatal("global guardrails must stay nil when reload aborts")
		}
	})
}

type bodySizeProcessor struct {
	extProcV3.UnimplementedExternalProcessorServer
	got chan int
}

func (p *bodySizeProcessor) Process(stream extProcV3.ExternalProcessor_ProcessServer) error {
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	p.got <- len(req.GetRequestBody().GetBody())
	return stream.Send(&extProcV3.ProcessingResponse{})
}

// sendBody sends one ext_proc request body of n bytes and returns the error
// from the reply, if any.
func sendBody(t *testing.T, n int) error {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := newGRPCServer()
	proc := &bodySizeProcessor{got: make(chan int, 1)}
	extProcV3.RegisterExternalProcessorServer(srv, proc)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	stream, err := extProcV3.NewExternalProcessorClient(conn).Process(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&extProcV3.ProcessingRequest{
		Request: &extProcV3.ProcessingRequest_RequestBody{
			RequestBody: &extProcV3.HttpBody{Body: bytes.Repeat([]byte{'x'}, n), EndOfStream: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if got := <-proc.got; got != n {
		t.Fatalf("received %d bytes, want %d", got, n)
	}
	return nil
}

// TestGRPCServer_RecvLimitCoversMaxBodyBytes guards against grpc's 4 MiB
// default rejecting a body the router would accept.
func TestGRPCServer_RecvLimitCoversMaxBodyBytes(t *testing.T) {
	t.Run("body at the default maxBodyBytes is accepted", func(t *testing.T) {
		if err := sendBody(t, int(config.DefaultMaxBodyBytes)); err != nil {
			t.Fatalf("body rejected: %v", err)
		}
	})
	t.Run("documented 10 MiB maxBodyBytes is accepted", func(t *testing.T) {
		if err := sendBody(t, 10<<20); err != nil {
			t.Fatalf("body rejected: %v", err)
		}
	})
	t.Run("body past the receive limit is rejected", func(t *testing.T) {
		if err := sendBody(t, grpcMaxRecvMsgSize+1); status.Code(err) != codes.ResourceExhausted {
			t.Fatalf("body past receive limit: got %v, want ResourceExhausted", err)
		}
	})
}
