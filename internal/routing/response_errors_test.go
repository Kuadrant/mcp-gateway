package routing

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestErrorResponseBuilders(t *testing.T) {
	builders := []struct {
		name  string
		build func(any, string) string
		sse   bool
		code  int
	}{
		{"SSE tool", BuildSSEToolExecutionError, true, 0},
		{"JSON tool", BuildJSONToolExecutionError, false, 0},
		{"SSE protocol", func(id any, message string) string {
			return BuildSSEProtocolRejection(id, -32002, message)
		}, true, -32002},
		{"JSON protocol", func(id any, message string) string {
			return BuildJSONProtocolRejection(id, -32602, message)
		}, false, -32602},
	}
	ids := []struct {
		name string
		id   any
		json string
	}{
		{"string", "req\"\\\n", `"req\"\\\n"`},
		{"number", 42, `42`},
		{"null", nil, `null`},
		{"unsupported type", make(chan int), `null`},
	}
	const message = "failed: \"quoted\" \\path\n\t雪"
	for _, builder := range builders {
		for _, id := range ids {
			t.Run(builder.name+"/"+id.name, func(t *testing.T) {
				body := builder.build(id.id, message)
				if builder.sse {
					require.True(t, strings.HasPrefix(body, "\nevent: message\ndata: "))
					require.True(t, strings.HasSuffix(body, "\n\n"))
					require.Equal(t, 1, strings.Count(body, "data: "))
					body = strings.TrimSuffix(strings.TrimPrefix(body, "\nevent: message\ndata: "), "\n\n")
				}
				var envelope map[string]json.RawMessage
				require.NoError(t, json.Unmarshal([]byte(body), &envelope))
				require.Len(t, envelope, 3)
				require.JSONEq(t, `"2.0"`, string(envelope["jsonrpc"]))
				require.JSONEq(t, id.json, string(envelope["id"]))
				if builder.code != 0 {
					require.NotContains(t, envelope, "result")
					var rpcError struct {
						Code    int    `json:"code"`
						Message string `json:"message"`
					}
					require.NoError(t, json.Unmarshal(envelope["error"], &rpcError))
					require.Equal(t, builder.code, rpcError.Code)
					require.Equal(t, message, rpcError.Message)
				} else {
					require.NotContains(t, envelope, "error")
					var result struct {
						IsError bool `json:"isError"`
						Content []struct {
							Type string `json:"type"`
							Text string `json:"text"`
						} `json:"content"`
					}
					require.NoError(t, json.Unmarshal(envelope["result"], &result))
					require.True(t, result.IsError)
					require.Len(t, result.Content, 1)
					require.Equal(t, "text", result.Content[0].Type)
					require.Equal(t, message, result.Content[0].Text)
				}
			})
		}
	}
}
