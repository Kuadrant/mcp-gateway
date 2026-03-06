package mcprouter

import (
	"testing"

	basepb "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	eppb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"
)

func TestResponseBuilder_WithRequestHeadersReponse(t *testing.T) {
	rb := NewResponse()
	headers := []*basepb.HeaderValueOption{
		{
			Header: &basepb.HeaderValue{
				Key:      ":authority",
				RawValue: []byte("example.com"),
			},
		},
		{
			Header: &basepb.HeaderValue{
				Key:      "x-custom-header",
				RawValue: []byte("test-value"),
			},
		},
	}

	rb.WithRequestHeadersReponse(headers)
	responses := rb.Build()
	require.Len(t, responses, 1)
	require.IsType(t, &eppb.ProcessingResponse_RequestHeaders{}, responses[0].Response)
	rh := responses[0].Response.(*eppb.ProcessingResponse_RequestHeaders)
	require.NotNil(t, rh.RequestHeaders)
	require.NotNil(t, rh.RequestHeaders.Response)
	require.True(t, rh.RequestHeaders.Response.ClearRouteCache)
	require.NotNil(t, rh.RequestHeaders.Response.HeaderMutation)
	require.Len(t, rh.RequestHeaders.Response.HeaderMutation.SetHeaders, 2)
	require.Equal(t, ":authority", rh.RequestHeaders.Response.HeaderMutation.SetHeaders[0].Header.Key)
	require.Equal(t, []byte("example.com"), rh.RequestHeaders.Response.HeaderMutation.SetHeaders[0].Header.RawValue)
}

func TestResponseBuilder_WithRequestBodyHeadersAndBodyReponse(t *testing.T) {
	rb := NewResponse()
	headers := []*basepb.HeaderValueOption{
		{
			Header: &basepb.HeaderValue{
				Key:      "content-type",
				RawValue: []byte("application/json"),
			},
		},
	}
	body := []byte(`{"test":"data"}`)

	rb.WithRequestBodyHeadersAndBodyReponse(headers, body)
	responses := rb.Build()
	require.Len(t, responses, 1)

	require.IsType(t, &eppb.ProcessingResponse_RequestBody{}, responses[0].Response)
	rbody := responses[0].Response.(*eppb.ProcessingResponse_RequestBody)
	require.NotNil(t, rbody.RequestBody)
	require.NotNil(t, rbody.RequestBody.Response)
	require.True(t, rbody.RequestBody.Response.ClearRouteCache)

	require.NotNil(t, rbody.RequestBody.Response.HeaderMutation)
	require.Len(t, rbody.RequestBody.Response.HeaderMutation.SetHeaders, 1)
	require.Equal(t, "content-type", rbody.RequestBody.Response.HeaderMutation.SetHeaders[0].Header.Key)

	require.NotNil(t, rbody.RequestBody.Response.BodyMutation)
	require.Equal(t, body, rbody.RequestBody.Response.BodyMutation.GetBody())
}

func TestResponseBuilder_WithRequestBodyHeadersResponse(t *testing.T) {
	rb := NewResponse()
	headers := []*basepb.HeaderValueOption{
		{
			Header: &basepb.HeaderValue{
				Key:      "x-mcp-method",
				RawValue: []byte("tools/call"),
			},
		},
	}

	rb.WithRequestBodyHeadersResponse(headers)
	responses := rb.Build()
	require.Len(t, responses, 1)

	require.IsType(t, &eppb.ProcessingResponse_RequestBody{}, responses[0].Response)
	rbody := responses[0].Response.(*eppb.ProcessingResponse_RequestBody)
	require.NotNil(t, rbody.RequestBody)
	require.NotNil(t, rbody.RequestBody.Response)
	require.True(t, rbody.RequestBody.Response.ClearRouteCache)

	require.NotNil(t, rbody.RequestBody.Response.HeaderMutation)
	require.Len(t, rbody.RequestBody.Response.HeaderMutation.SetHeaders, 1)
	require.Equal(t, "x-mcp-method", rbody.RequestBody.Response.HeaderMutation.SetHeaders[0].Header.Key)

	require.Nil(t, rbody.RequestBody.Response.BodyMutation)
}

func TestResponseBuilder_WithImmediateResponse(t *testing.T) {
	testCases := []struct {
		Name       string
		StatusCode int32
		Message    string
	}{
		{
			Name:       "400 bad request",
			StatusCode: 400,
			Message:    "invalid request",
		},
		{
			Name:       "404 not found",
			StatusCode: 404,
			Message:    "not found",
		},
		{
			Name:       "500 internal error",
			StatusCode: 500,
			Message:    "internal server error",
		},
		{
			Name:       "502 bad gateway",
			StatusCode: 502,
			Message:    "session lookup failed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			rb := NewResponse()
			rb.WithImmediateResponse(tc.StatusCode, tc.Message)

			responses := rb.Build()
			require.Len(t, responses, 1)

			require.IsType(t, &eppb.ProcessingResponse_ImmediateResponse{}, responses[0].Response)
			ir := responses[0].Response.(*eppb.ProcessingResponse_ImmediateResponse)
			require.NotNil(t, ir.ImmediateResponse)
			require.Equal(t, tc.StatusCode, int32(ir.ImmediateResponse.Status.Code))
			require.Equal(t, []byte(tc.Message), ir.ImmediateResponse.Body)
			require.Equal(t, "ext-proc error: "+tc.Message, ir.ImmediateResponse.Details)
		})
	}
}

func TestResponseBuilder_WithStreamingResponse(t *testing.T) {
	rb := NewResponse()
	headers := []*basepb.HeaderValueOption{
		{
			Header: &basepb.HeaderValue{
				Key:      "content-length",
				RawValue: []byte("100"),
			},
		},
	}
	body := []byte(`{"streaming":"data"}`)

	rb.WithStreamingResponse(headers, body)
	responses := rb.Build()
	require.Len(t, responses, 1)

	require.IsType(t, &eppb.ProcessingResponse_RequestBody{}, responses[0].Response)
	rbody := responses[0].Response.(*eppb.ProcessingResponse_RequestBody)
	require.NotNil(t, rbody.RequestBody)
	require.NotNil(t, rbody.RequestBody.Response)

	require.NotNil(t, rbody.RequestBody.Response.HeaderMutation)
	require.Len(t, rbody.RequestBody.Response.HeaderMutation.SetHeaders, 1)

	require.NotNil(t, rbody.RequestBody.Response.BodyMutation)
	streamedResponse := rbody.RequestBody.Response.BodyMutation.GetStreamedResponse()
	require.NotNil(t, streamedResponse)
	require.Equal(t, body, streamedResponse.Body)
	require.True(t, streamedResponse.EndOfStream)
}

func TestResponseBuilder_WithDoNothingResponse(t *testing.T) {
	testCases := []struct {
		Name        string
		IsStreaming bool
	}{
		{
			Name:        "streaming mode",
			IsStreaming: true,
		},
		{
			Name:        "non-streaming mode",
			IsStreaming: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			rb := NewResponse()
			rb.WithDoNothingResponse(tc.IsStreaming)
			responses := rb.Build()
			require.Len(t, responses, 1)

			if tc.IsStreaming {
				require.IsType(t, &eppb.ProcessingResponse_RequestHeaders{}, responses[0].Response)
				rh := responses[0].Response.(*eppb.ProcessingResponse_RequestHeaders)
				require.NotNil(t, rh.RequestHeaders)
			} else {
				require.IsType(t, &eppb.ProcessingResponse_RequestBody{}, responses[0].Response)
				rbody := responses[0].Response.(*eppb.ProcessingResponse_RequestBody)
				require.NotNil(t, rbody.RequestBody)
			}
		})
	}
}

func TestResponseBuilder_ChainedCalls(t *testing.T) {
	rb := NewResponse()
	headers := []*basepb.HeaderValueOption{
		{
			Header: &basepb.HeaderValue{
				Key:      "x-test",
				RawValue: []byte("value"),
			},
		},
	}

	rb.WithRequestHeadersReponse(headers).
		WithImmediateResponse(400, "bad request")

	responses := rb.Build()
	require.Len(t, responses, 2)

	require.IsType(t, &eppb.ProcessingResponse_RequestHeaders{}, responses[0].Response)
	require.IsType(t, &eppb.ProcessingResponse_ImmediateResponse{}, responses[1].Response)
}

func TestResponseBuilder_EmptyHeaders(t *testing.T) {
	rb := NewResponse()
	emptyHeaders := []*basepb.HeaderValueOption{}

	rb.WithRequestHeadersReponse(emptyHeaders)

	responses := rb.Build()
	require.Len(t, responses, 1)

	require.IsType(t, &eppb.ProcessingResponse_RequestHeaders{}, responses[0].Response)
	rh := responses[0].Response.(*eppb.ProcessingResponse_RequestHeaders)
	require.NotNil(t, rh.RequestHeaders.Response.HeaderMutation)
	require.Len(t, rh.RequestHeaders.Response.HeaderMutation.SetHeaders, 0)
}

func TestResponseBuilder_EmptyBody(t *testing.T) {
	rb := NewResponse()
	headers := []*basepb.HeaderValueOption{}
	emptyBody := []byte{}

	rb.WithRequestBodyHeadersAndBodyReponse(headers, emptyBody)

	responses := rb.Build()
	require.Len(t, responses, 1)

	require.IsType(t, &eppb.ProcessingResponse_RequestBody{}, responses[0].Response)
	rbody := responses[0].Response.(*eppb.ProcessingResponse_RequestBody)
	require.Equal(t, emptyBody, rbody.RequestBody.Response.BodyMutation.GetBody())
}
