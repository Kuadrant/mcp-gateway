package routing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// mcpRequestMembers are the top-level JSON-RPC members decoded into MCPRequest.
var mcpRequestMembers = [][]byte{[]byte("jsonrpc"), []byte("id"), []byte("method"), []byte("params"), []byte("result")}

// CheckMCPRequestMembers rejects an MCP request body that repeats an
// MCPRequest member under any case variant. See CheckDuplicateMembers.
func CheckMCPRequestMembers(body []byte) error {
	return CheckDuplicateMembers(body, mcpRequestMembers)
}

// CheckDuplicateMembers rejects a JSON object whose top level has more than one
// member matching the same name in members, compared the way encoding/json
// matches struct fields (case-insensitive, Unicode simple folding).
// encoding/json merges such members ("params" and "PARAMS" both fill
// MCPRequest.Params) while MCP SDKs decode case-sensitively, so the router and
// the upstream would otherwise act on different values. body must be valid JSON.
func CheckDuplicateMembers(body []byte, members [][]byte) error {
	var seen uint64
	depth := 0
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case '"':
			start := i
			escaped := false
			for i++; i < len(body) && body[i] != '"'; i++ {
				if body[i] == '\\' {
					escaped = true
					i++
				}
			}
			if depth != 1 || !followedByColon(body, i+1) {
				continue
			}
			name := body[start+1 : i]
			if escaped {
				var s string
				if err := json.Unmarshal(body[start:i+1], &s); err != nil {
					return errors.Join(ErrInvalidRequest, err)
				}
				name = []byte(s)
			}
			for m := range members {
				if !bytes.EqualFold(name, members[m]) {
					continue
				}
				if seen&(1<<m) != 0 {
					return errors.Join(ErrInvalidRequest, fmt.Errorf("duplicate %q member in json rpc payload", members[m]))
				}
				seen |= 1 << m
			}
		}
	}
	return nil
}

// followedByColon reports whether the next non-whitespace byte from i is ':'.
func followedByColon(body []byte, i int) bool {
	for ; i < len(body); i++ {
		switch body[i] {
		case ' ', '\t', '\n', '\r':
			continue
		case ':':
			return true
		default:
			return false
		}
	}
	return false
}
