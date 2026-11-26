package routing

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckMCPRequestMembers(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"plain request", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"t","arguments":{"q":1}}}`, false},
		{"whitespace around colons", "{\"jsonrpc\" : \"2.0\",\n\"id\"\t:1, \"method\"\r\n: \"ping\"}", false},
		{"upper-case variant after", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"t"},"PARAMS":{"name":"t"}}`, true},
		{"mixed-case variant before", `{"Params":{"name":"t"},"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"t"}}`, true},
		{"exact duplicate", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"a"},"params":{"name":"b"}}`, true},
		{"method variant", `{"jsonrpc":"2.0","id":1,"method":"tools/list","Method":"tools/call"}`, true},
		{"id variant", `{"jsonrpc":"2.0","id":1,"ID":2,"method":"ping"}`, true},
		{"jsonrpc variant", `{"jsonrpc":"2.0","JSONRPC":"2.0","id":1,"method":"ping"}`, true},
		{"result variant", `{"jsonrpc":"2.0","id":1,"result":{"action":"accept"},"Result":{"action":"decline"}}`, true},
		{"escaped variant", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{},"PAR\u0041MS":{}}`, true},
		{"unicode fold variant", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{},"param\u017f":{}}`, true},
		{"lone case variant", `{"jsonrpc":"2.0","id":1,"method":"tools/call","PARAMS":{"name":"t"}}`, false},
		{"nested variants are not top level", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"a","NAME":"b","params":{},"PARAMS":{}}}`, false},
		{"unknown member repeated", `{"jsonrpc":"2.0","id":1,"method":"ping","x":1,"X":2}`, false},
		{"member names inside string values", `{"jsonrpc":"2.0","id":"{\"id\":","method":"params","params":{"q":"\"params\":{}}"}}`, false},
		{"top-level array", `[{"id":1},{"id":2}]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.True(t, json.Valid([]byte(tc.body)), "test body must be valid json")
			err := CheckMCPRequestMembers([]byte(tc.body))
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidRequest)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// documents why the check exists: encoding/json folds these into one field.
func TestEncodingJSONMergesCaseVariants(t *testing.T) {
	var req MCPRequest
	require.NoError(t, json.Unmarshal([]byte(`{"params":{"arguments":"real"},"param\u017f":{"arguments":"decoy"}}`), &req))
	require.Equal(t, "decoy", req.Params["arguments"])
}

func BenchmarkCheckMCPRequestMembers(b *testing.B) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"server1_search","arguments":{"query":"kubernetes gateway","limit":10},"_meta":{"progressToken":1}}}`)
	b.ReportAllocs()
	for b.Loop() {
		if err := CheckMCPRequestMembers(body); err != nil {
			b.Fatal(err)
		}
	}
}
