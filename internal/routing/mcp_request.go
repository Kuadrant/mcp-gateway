package routing

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	sharedheaders "github.com/Kuadrant/mcp-gateway/internal/headers"
)

// ErrInvalidRequest is an error for an invalid request
var ErrInvalidRequest = errors.New("MCP Request is invalid")

// RouterError represents an error with an associated HTTP status code
type RouterError struct {
	StatusCode int32
	Err        error
}

func (re *RouterError) Error() string {
	if re.Err != nil {
		return re.Err.Error()
	}
	return fmt.Sprintf("router error: status %d", re.StatusCode)
}

func (re *RouterError) Unwrap() error { return re.Err }

// Code returns http status code
func (re *RouterError) Code() int32 { return re.StatusCode }

// NewRouterError creates router error with status code
func NewRouterError(code int32, err error) *RouterError {
	return &RouterError{StatusCode: code, Err: err}
}

// NewRouterErrorf creates router error with formatted message
func NewRouterErrorf(code int32, format string, args ...any) *RouterError {
	return &RouterError{StatusCode: code, Err: fmt.Errorf(format, args...)}
}

// mcp json-rpc method names and elicitation constants
const (
	MethodToolCall     = "tools/call"
	MethodPromptGet    = "prompts/get"
	MethodResourceRead = "resources/read"
	MethodInitialize   = "initialize"

	elicitationResultAction  = "action"
	elicitationActionAccept  = "accept"
	elicitationActionDecline = "decline"
	elicitationActionCancel  = "cancel"
)

// Header constants used by the routing layer.
const (
	MCPServerNameHeader   = "x-mcp-servername"
	ToolAnnotationsHeader = "x-mcp-annotation-hints"
	ToolHeader            = "x-mcp-toolname"
	PromptHeader          = "x-mcp-promptname"
	ResourceHeader        = "x-mcp-resourceuri"
	MethodHeader          = "x-mcp-method"
	SessionHeader         = "mcp-session-id"
	AuthorityHeader       = ":authority"
	AuthorizationHeader   = "authorization"

	// RoutingKey is an internal header used to authenticate a request from the router
	RoutingKey = "router-key"

	// broker-only filtering headers that must not reach upstream servers
	MCPAuthorizedHeader    = "x-mcp-authorized"
	MCPVirtualServerHeader = "x-mcp-virtualserver"
)

// MCPVerifiedSubHeader carries the JWT sub the router verified via AuthPolicy.
var MCPVerifiedSubHeader = sharedheaders.VerifiedSubHeader

// InternalOnlyHeaders are headers used internally by the gateway for filtering
// and routing that must be stripped before forwarding to upstream MCP servers.
var InternalOnlyHeaders = []string{MCPAuthorizedHeader, MCPVirtualServerHeader, MCPVerifiedSubHeader}

// MCPRequest encapsulates a mcp protocol request to the gateway
type MCPRequest struct {
	ID                any               `json:"id"`
	JSONRPC           string            `json:"jsonrpc"`
	Method            string            `json:"method,omitempty"`
	Params            map[string]any    `json:"params,omitempty"`
	Result            map[string]any    `json:"result,omitempty"`
	Headers           map[string]string `json:"-"`
	SessionID         string            `json:"-"`
	ServerName        string            `json:"-"`
	ServerPrefix      string            `json:"-"`
	BackendSessionID  string            `json:"-"`
	ClientElicitation bool              `json:"-"`
}

// GetSingleHeaderValue returns header value by key
func (mr *MCPRequest) GetSingleHeaderValue(key string) string {
	return mr.Headers[key]
}

// GetSessionID returns cached or header session id
func (mr *MCPRequest) GetSessionID() string {
	if mr.SessionID == "" {
		mr.SessionID = mr.Headers[SessionHeader]
	}
	return mr.SessionID
}

// Validate checks json-rpc structure and required fields
func (mr *MCPRequest) Validate() (bool, error) {
	if mr == nil {
		return false, errors.Join(ErrInvalidRequest, fmt.Errorf("JSON invalid"))
	}
	if mr.JSONRPC != "2.0" {
		return false, errors.Join(ErrInvalidRequest, fmt.Errorf("json rpc version invalid"))
	}
	if mr.Method == "" && !mr.IsElicitationResponse() {
		return false, errors.Join(ErrInvalidRequest, fmt.Errorf("no method set in json rpc payload"))
	}
	if mr.ID == nil && !mr.IsNotificationRequest() {
		return false, errors.Join(ErrInvalidRequest, fmt.Errorf("no id set in json rpc payload for none notification method: %s ", mr.Method))
	}
	return true, nil
}

// IsNotificationRequest checks if method starts with notifications prefix
func (mr *MCPRequest) IsNotificationRequest() bool {
	return strings.HasPrefix(mr.Method, "notifications")
}

// IsToolCall checks if method is tools/call
func (mr *MCPRequest) IsToolCall() bool {
	return mr.Method == MethodToolCall
}

// IsInitializeRequest checks if method is initialize or notifications/initialized
func (mr *MCPRequest) IsInitializeRequest() bool {
	return mr.Method == "initialize" || mr.Method == "notifications/initialized"
}

// ClientSupportsElicitation checks if client declared elicitation capability
func (mr *MCPRequest) ClientSupportsElicitation() bool {
	if mr.Method != MethodInitialize || mr.Params == nil {
		return false
	}
	caps, ok := mr.Params["capabilities"]
	if !ok {
		return false
	}
	capsMap, ok := caps.(map[string]any)
	if !ok {
		return false
	}
	_, hasElicitation := capsMap["elicitation"]
	return hasElicitation
}

// IsElicitationResponse checks if result contains accept/decline/cancel action
func (mr *MCPRequest) IsElicitationResponse() bool {
	if mr.Method != "" || mr.Result == nil {
		return false
	}
	action, ok := mr.Result[elicitationResultAction]
	if !ok {
		return false
	}
	a, ok := action.(string)
	if !ok {
		return false
	}
	return a == elicitationActionAccept || a == elicitationActionDecline || a == elicitationActionCancel
}

// ToolName extracts tool name from tools/call params
func (mr *MCPRequest) ToolName() string {
	if !mr.IsToolCall() {
		return ""
	}
	tool, ok := mr.Params["name"]
	if !ok {
		return ""
	}
	t, ok := tool.(string)
	if !ok {
		return ""
	}
	return t
}

// ReWriteToolName replaces tool name in params
func (mr *MCPRequest) ReWriteToolName(actualTool string) {
	if mr.Params == nil {
		mr.Params = map[string]any{}
	}
	mr.Params["name"] = actualTool
}

// IsPromptGet checks if method is prompts/get
func (mr *MCPRequest) IsPromptGet() bool {
	return mr.Method == MethodPromptGet
}

// PromptName extracts prompt name from prompts/get params
func (mr *MCPRequest) PromptName() string {
	if !mr.IsPromptGet() {
		return ""
	}
	prompt, ok := mr.Params["name"]
	if !ok {
		return ""
	}
	p, ok := prompt.(string)
	if !ok {
		return ""
	}
	return p
}

// ReWritePromptName replaces prompt name in params
func (mr *MCPRequest) ReWritePromptName(actualPrompt string) {
	if mr.Params == nil {
		mr.Params = map[string]any{}
	}
	mr.Params["name"] = actualPrompt
}

// IsResourceRead checks if method is resources/read
func (mr *MCPRequest) IsResourceRead() bool {
	return mr.Method == MethodResourceRead
}

// ResourceURI extracts the resource uri from resources/read params
func (mr *MCPRequest) ResourceURI() string {
	if !mr.IsResourceRead() {
		return ""
	}
	uri, ok := mr.Params["uri"]
	if !ok {
		return ""
	}
	u, ok := uri.(string)
	if !ok {
		return ""
	}
	return u
}

// ReWriteResourceURI replaces the resource uri in params
func (mr *MCPRequest) ReWriteResourceURI(actualURI string) {
	if mr.Params == nil {
		mr.Params = map[string]any{}
	}
	mr.Params["uri"] = actualURI
}

// ToBytes marshals request to json
func (mr *MCPRequest) ToBytes() ([]byte, error) {
	return json.Marshal(mr)
}

// ElicitationInfo holds the data needed to route an elicitation request to the broker.
type ElicitationInfo struct {
	RequestID     string
	ElicitationID string
}



// ResourceAuthority extracts the authority segment (host) from a resource URI.
// For malformed URIs, returns the URI unchanged.
func ResourceAuthority(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	return u.Host
}

// EnsureSeparator returns prefix with trailing underscore, adding one if needed.
// Empty prefix is returned unchanged.
func EnsureSeparator(prefix string) string {
	if prefix == "" {
		return prefix
	}
	if !strings.HasSuffix(prefix, "_") {
		return prefix + "_"
	}
	return prefix
}

// InjectResourcePrefix injects prefix into a ui:// URI's authority segment
// (ui://template.html -> ui://<prefix_>template.html). Non-ui:// and
// malformed URIs are returned unchanged with ok=false.
func InjectResourcePrefix(uri, prefix string) (rewritten string, ok bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "ui" {
		return uri, false
	}
	u.User = nil // never forward upstream credentials to clients
	u.Host = EnsureSeparator(prefix) + u.Host
	return u.String(), true
}

// StripResourcePrefix removes prefix from a ui:// URI's authority segment,
// the inverse of InjectResourcePrefix. Non-ui:// and malformed URIs are
// returned unchanged.
func StripResourcePrefix(uri, prefix string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "ui" || u.Host == "" {
		return uri
	}
	u.Host = StripAuthorityPrefix(u.Host, prefix)
	return u.String()
}

// StripAuthorityPrefix removes prefix and its separator from an authority
// string. For authority "app_example.com" with prefix "app", returns
// "example.com". If the authority doesn't start with the prefix, it's
// returned unchanged.
func StripAuthorityPrefix(authority, prefix string) string {
	if prefix == "" {
		return authority
	}
	return strings.TrimPrefix(authority, EnsureSeparator(prefix))
}
