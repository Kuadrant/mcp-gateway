package mcprouter

import (
	"fmt"

	"github.com/Kuadrant/mcp-gateway/internal/routing"
	basepb "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
)

func getSingleValueHeader(headers *basepb.HeaderMap, name string) string {
	if headers == nil {
		return ""
	}
	for _, hk := range headers.Headers {
		if hk != nil && hk.Key == name {
			return string(hk.RawValue)
		}
	}
	return ""
}

// HeadersBuilder builds headers to add to the request or response
type HeadersBuilder struct {
	headers []*basepb.HeaderValueOption
}

// NewHeaders returns a new HeadersBuilder
func NewHeaders() *HeadersBuilder {
	return &HeadersBuilder{
		headers: []*basepb.HeaderValueOption{},
	}
}

// Build will build the header ready to be added to a request or response
func (hb *HeadersBuilder) Build() []*basepb.HeaderValueOption {
	return hb.headers
}

// WithAuthority will set the :authority header
func (hb *HeadersBuilder) WithAuthority(authority string) *HeadersBuilder {
	hb.headers = append(hb.headers, &basepb.HeaderValueOption{
		Header: &basepb.HeaderValue{
			Key:      routing.AuthorityHeader,
			RawValue: []byte(authority),
		},
	})
	return hb
}

// WithAuth will set the authorization header
func (hb *HeadersBuilder) WithAuth(cred string) *HeadersBuilder {
	hb.headers = append(hb.headers, &basepb.HeaderValueOption{
		Header: &basepb.HeaderValue{
			Key:      routing.AuthorizationHeader,
			RawValue: []byte(cred),
		},
	})
	return hb
}

// WithContentLength will set the content-length header
func (hb *HeadersBuilder) WithContentLength(length int) *HeadersBuilder {
	hb.headers = append(hb.headers, &basepb.HeaderValueOption{
		Header: &basepb.HeaderValue{
			Key:      "content-length",
			RawValue: []byte(fmt.Sprintf("%d", length)),
		},
	})
	return hb
}

// WithMCPToolName will set the x-mcp-toolname header
func (hb *HeadersBuilder) WithMCPToolName(toolName string) *HeadersBuilder {
	hb.headers = append(hb.headers, &basepb.HeaderValueOption{
		Header: &basepb.HeaderValue{
			Key:      routing.ToolHeader,
			RawValue: []byte(toolName),
		},
	})
	return hb
}

// WithMCPServerName will set the x-mcp-servername header
func (hb *HeadersBuilder) WithMCPServerName(serverName string) *HeadersBuilder {
	hb.headers = append(hb.headers, &basepb.HeaderValueOption{
		Header: &basepb.HeaderValue{
			Key:      routing.MCPServerNameHeader,
			RawValue: []byte(serverName),
		},
	})
	return hb
}

// WithMCPMethod will set the x-mcp-method header
func (hb *HeadersBuilder) WithMCPMethod(method string) *HeadersBuilder {
	hb.headers = append(hb.headers, &basepb.HeaderValueOption{
		Header: &basepb.HeaderValue{
			Key:      routing.MethodHeader,
			RawValue: []byte(method),
		},
	})
	return hb
}

// WithMCPSession will set the mcp-session-id header
func (hb *HeadersBuilder) WithMCPSession(session string) *HeadersBuilder {
	hb.headers = append(hb.headers, &basepb.HeaderValueOption{
		Header: &basepb.HeaderValue{
			Key:      routing.SessionHeader,
			RawValue: []byte(session),
		},
	})
	return hb
}

// WithToolAnnotations will set the x-mcp-annotation-hints header
func (hb *HeadersBuilder) WithToolAnnotations(annotations string) *HeadersBuilder {
	hb.headers = append(hb.headers, &basepb.HeaderValueOption{
		Header: &basepb.HeaderValue{
			Key:      routing.ToolAnnotationsHeader,
			RawValue: []byte(annotations),
		},
	})
	return hb
}

// WithMCPPromptName will set the x-mcp-promptname header
func (hb *HeadersBuilder) WithMCPPromptName(promptName string) *HeadersBuilder {
	hb.headers = append(hb.headers, &basepb.HeaderValueOption{
		Header: &basepb.HeaderValue{
			Key:      routing.PromptHeader,
			RawValue: []byte(promptName),
		},
	})
	return hb
}

// WithCustomHeader will set key with value in the headers
func (hb *HeadersBuilder) WithCustomHeader(key, value string) *HeadersBuilder {
	hb.headers = append(hb.headers, &basepb.HeaderValueOption{
		Header: &basepb.HeaderValue{
			Key:      key,
			RawValue: []byte(value),
		},
	})
	return hb
}

// WithPath will set the :path header
func (hb *HeadersBuilder) WithPath(path string) *HeadersBuilder {
	hb.headers = append(hb.headers, &basepb.HeaderValueOption{
		Header: &basepb.HeaderValue{
			Key:      ":path",
			RawValue: []byte(path),
		},
	})
	return hb
}

// WithVerifiedSub sets the x-mcp-verified-sub header to the JWT sub claim
// extracted by the router after AuthPolicy verification. The broker reads this
// instead of decoding the raw JWT, so identity binding is always verified.
func (hb *HeadersBuilder) WithVerifiedSub(sub string) *HeadersBuilder {
	hb.headers = append(hb.headers, &basepb.HeaderValueOption{
		Header: &basepb.HeaderValue{
			Key:      routing.MCPVerifiedSubHeader,
			RawValue: []byte(sub),
		},
	})
	return hb
}
