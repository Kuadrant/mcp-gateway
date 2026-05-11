package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
)

// ServerValidationStatus contains the validation status of a single MCP server
type ServerValidationStatus struct {
	ID                     string                 `json:"id"`
	Name                   string                 `json:"name"`
	Prefix                 string                 `json:"prefix"`
	ConnectionStatus       ConnectionStatus       `json:"connectionStatus"`
	ProtocolValidation     ProtocolValidation     `json:"protocolValidation"`
	CapabilitiesValidation CapabilitiesValidation `json:"capabilitiesValidation"`
	ToolConflicts          []ToolConflict         `json:"toolConflicts"`
	LastValidated          time.Time              `json:"lastValidated"`
}

// ConnectionStatus represents the connection health of an MCP server
type ConnectionStatus struct {
	IsReachable    bool   `json:"isReachable"`
	Error          string `json:"error,omitempty"`
	HTTPStatusCode int    `json:"httpStatusCode,omitempty"`
}

// ProtocolValidation represents the MCP protocol version validation results
type ProtocolValidation struct {
	IsValid          bool   `json:"isValid"`
	SupportedVersion string `json:"supportedVersion"`
	ExpectedVersion  string `json:"expectedVersion"`
}

// CapabilitiesValidation represents the capabilities validation results
type CapabilitiesValidation struct {
	IsValid             bool     `json:"isValid"`
	HasToolCapabilities bool     `json:"hasToolCapabilities"`
	ToolCount           int      `json:"toolCount"`
	MissingCapabilities []string `json:"missingCapabilities"`
}

// ToolConflict represents a tool name conflict between servers
type ToolConflict struct {
	ToolName      string   `json:"toolName"`
	PrefixedName  string   `json:"prefixedName"`
	ConflictsWith []string `json:"conflictsWith"`
}

// StatusResponse contains the overall validation status of all servers
type StatusResponse struct {
	Servers          []upstream.ServerValidationStatus `json:"servers"`
	OverallValid     bool                              `json:"overallValid"`
	TotalServers     int                               `json:"totalServers"`
	HealthyServers   int                               `json:"healthyServers"`
	UnHealthyServers int                               `json:"unHealthyServers"`
	ToolConflicts    int                               `json:"toolConflicts"`
	Timestamp        time.Time                         `json:"timestamp"`
}

// StatusHandler handles HTTP requests to the status endpoint
type StatusHandler struct {
	broker MCPBroker
	logger slog.Logger
}

// NewStatusHandler creates a new status handler for HTTP status endpoints
func NewStatusHandler(broker MCPBroker, logger slog.Logger) *StatusHandler {
	return &StatusHandler{
		broker: broker,
		logger: logger,
	}
}

// ServeHTTP implements http.Handler interface
func (h *StatusHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.setResponseHeaders(w, r)

	switch r.Method {
	case http.MethodGet:
		h.handleGetStatus(w, r)
	default:
		h.sendErrorResponse(w, http.StatusMethodNotAllowed, "Method not allowed. Supported methods: GET")
	}
}

func (h *StatusHandler) setResponseHeaders(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func (h *StatusHandler) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// Parse URL path to check for specific server request
	path := strings.TrimPrefix(r.URL.Path, "/status")
	if path != "" && path != "/" {
		// Remove leading slash and extract server name
		serverName := strings.TrimPrefix(path, "/")
		if serverName != "" {
			h.handleSingleServerByName(ctx, w, serverName)
			return
		}
	}
	response := h.broker.ValidateAllServers()
	h.sendJSONResponse(w, http.StatusOK, response)
}

func (h *StatusHandler) handleSingleServerByName(_ context.Context, w http.ResponseWriter, serverName string) {
	//TODO(craig) this should not need to call validate all servers
	statusResponse := h.broker.ValidateAllServers()

	var serverStatus *upstream.ServerValidationStatus

	// Only support exact match (full namespace/route format)
	for _, server := range statusResponse.Servers {
		if server.Name == serverName {
			serverStatus = &server
			break
		}
	}

	if serverStatus == nil {
		h.sendErrorResponse(w, http.StatusNotFound, fmt.Sprintf("Server '%s' not found. Use format 'namespace/route-name' or check available servers at /status", serverName))
		return
	}

	h.logger.Info("Retrieved status for specific server", "serverName", serverName)
	h.sendJSONResponse(w, http.StatusOK, serverStatus)
}

func (h *StatusHandler) sendJSONResponse(w http.ResponseWriter, statusCode int, data any) {
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		h.logger.Error("Failed to encode JSON response", "error", err)
	}
}

func (h *StatusHandler) sendErrorResponse(w http.ResponseWriter, statusCode int, message string) {
	response := map[string]string{"error": message}
	h.sendJSONResponse(w, statusCode, response)
}
