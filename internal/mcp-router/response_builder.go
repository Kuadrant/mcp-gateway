package mcprouter

import (
	"fmt"

	basepb "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	eppb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typepb "github.com/envoyproxy/go-control-plane/envoy/type/v3"
)

// ResponseBuilder builds envoy external processor responses
type ResponseBuilder struct {
	response []*eppb.ProcessingResponse
}

// WithRequestHeadersReponse adds a request headers response with header mutations, clears route cache
func (rb *ResponseBuilder) WithRequestHeadersReponse(headers []*basepb.HeaderValueOption) *ResponseBuilder {
	rb.response = append(rb.response, &eppb.ProcessingResponse{
		Response: &eppb.ProcessingResponse_RequestHeaders{
			RequestHeaders: &eppb.HeadersResponse{
				Response: &eppb.CommonResponse{
					ClearRouteCache: true,
					HeaderMutation: &eppb.HeaderMutation{
						SetHeaders: headers,
					},
				},
			},
		},
	})
	return rb
}

// WithRequestBodyHeadersAndBodyReponse adds request body response with header and body mutations, clears route cache
func (rb *ResponseBuilder) WithRequestBodyHeadersAndBodyReponse(headers []*basepb.HeaderValueOption, body []byte) *ResponseBuilder {
	rb.response = append(rb.response, &eppb.ProcessingResponse{
		Response: &eppb.ProcessingResponse_RequestBody{
			RequestBody: &eppb.BodyResponse{
				Response: &eppb.CommonResponse{
					// Necessary so that the new headers are used in the routing decision.
					ClearRouteCache: true,
					HeaderMutation: &eppb.HeaderMutation{
						SetHeaders: headers,
					},
					BodyMutation: &eppb.BodyMutation{
						Mutation: &eppb.BodyMutation_Body{
							Body: body,
						},
					},
				},
			},
		},
	})
	return rb
}

// WithRequestBodyHeadersResponse adds request body response with header mutations only, clears route cache
func (rb *ResponseBuilder) WithRequestBodyHeadersResponse(headers []*basepb.HeaderValueOption) *ResponseBuilder {
	rb.response = append(rb.response, &eppb.ProcessingResponse{
		Response: &eppb.ProcessingResponse_RequestBody{
			RequestBody: &eppb.BodyResponse{
				Response: &eppb.CommonResponse{
					// Necessary so that the new headers are used in the routing decision.
					ClearRouteCache: true,
					HeaderMutation: &eppb.HeaderMutation{
						SetHeaders: headers,
					},
				},
			},
		},
	})
	return rb
}

// WithRequestBodySetUnsetHeadersResponse will set and unset headers in the request
func (rb *ResponseBuilder) WithRequestBodySetUnsetHeadersResponse(set []*basepb.HeaderValueOption, unset []string) *ResponseBuilder {
	rb.response = append(rb.response, &eppb.ProcessingResponse{
		Response: &eppb.ProcessingResponse_RequestBody{
			RequestBody: &eppb.BodyResponse{
				Response: &eppb.CommonResponse{
					// Necessary so that the new headers are used in the routing decision.
					ClearRouteCache: true,
					HeaderMutation: &eppb.HeaderMutation{
						SetHeaders:    set,
						RemoveHeaders: unset,
					},
				},
			},
		},
	})
	return rb
}

// WithImmediateResponse adds an immediate error response that terminates request processing
func (rb *ResponseBuilder) WithImmediateResponse(statusCode int32, message string) *ResponseBuilder {
	rb.response = append(rb.response, &eppb.ProcessingResponse{
		Response: &eppb.ProcessingResponse_ImmediateResponse{
			ImmediateResponse: &eppb.ImmediateResponse{
				Status: &typepb.HttpStatus{
					Code: typepb.StatusCode(statusCode),
				},
				Body:    []byte(message),
				Details: fmt.Sprintf("ext-proc error: %s", message),
			},
		},
	})
	return rb
}

// WithImmediateJSONRPCResponse adds an immediate response, typically JSON-RPC format,
// that terminates request processing
func (rb *ResponseBuilder) WithImmediateJSONRPCResponse(statusCode int32, setHeaders []*basepb.HeaderValueOption, message string) *ResponseBuilder {
	rb.response = append(rb.response, &eppb.ProcessingResponse{
		Response: &eppb.ProcessingResponse_ImmediateResponse{
			ImmediateResponse: &eppb.ImmediateResponse{
				Status: &typepb.HttpStatus{
					Code: typepb.StatusCode(statusCode),
				},
				Body: []byte(message),
				Headers: &eppb.HeaderMutation{
					SetHeaders: setHeaders,
				},
				Details: fmt.Sprintf("ext-proc error: %s", message),
			},
		},
	})
	return rb
}

// WithStreamingResponse adds a streaming request body response with headers
func (rb *ResponseBuilder) WithStreamingResponse(headers []*basepb.HeaderValueOption, body []byte) *ResponseBuilder {
	rb.response = append(rb.response, &eppb.ProcessingResponse{
		Response: &eppb.ProcessingResponse_RequestBody{
			RequestBody: &eppb.BodyResponse{
				Response: &eppb.CommonResponse{
					HeaderMutation: &eppb.HeaderMutation{SetHeaders: headers},
					BodyMutation: &eppb.BodyMutation{
						Mutation: &eppb.BodyMutation_StreamedResponse{
							StreamedResponse: &eppb.StreamedBodyResponse{
								Body:        body,
								EndOfStream: true,
							},
						},
					},
				},
			},
		},
	})
	return rb
}

// WithDoNothingResponse adds an empty response that allows request to continue unmodified
func (rb *ResponseBuilder) WithDoNothingResponse(isStreaming bool) *ResponseBuilder {
	if isStreaming {
		rb.response = append(rb.response, &eppb.ProcessingResponse{
			Response: &eppb.ProcessingResponse_RequestHeaders{
				RequestHeaders: &eppb.HeadersResponse{},
			},
		})
	} else {
		rb.response = append(rb.response, &eppb.ProcessingResponse{
			Response: &eppb.ProcessingResponse_RequestBody{
				RequestBody: &eppb.BodyResponse{},
			},
		})
	}

	return rb
}

// WithDoNothingResponseHeaderResponse will return a processing response that makes no changes
func (rb *ResponseBuilder) WithDoNothingResponseHeaderResponse() *ResponseBuilder {
	rb.response = append(rb.response, &eppb.ProcessingResponse{
		Response: &eppb.ProcessingResponse_ResponseHeaders{
			ResponseHeaders: &eppb.HeadersResponse{},
		},
	})
	return rb
}

// WithResponseHeaderResponse will return a processing response to set the headers passed into the response
func (rb *ResponseBuilder) WithResponseHeaderResponse(headers []*basepb.HeaderValueOption) *ResponseBuilder {
	rb.response = append(rb.response, &eppb.ProcessingResponse{
		Response: &eppb.ProcessingResponse_ResponseHeaders{
			ResponseHeaders: &eppb.HeadersResponse{
				Response: &eppb.CommonResponse{
					HeaderMutation: &eppb.HeaderMutation{
						SetHeaders: headers,
					},
				},
			},
		},
	})
	return rb
}

// Build returns the accumulated processing responses
func (rb *ResponseBuilder) Build() []*eppb.ProcessingResponse {
	return rb.response
}

// NewResponse creates a new response builder
func NewResponse() *ResponseBuilder {
	return &ResponseBuilder{
		response: []*eppb.ProcessingResponse{},
	}
}
