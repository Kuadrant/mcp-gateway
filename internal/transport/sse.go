package transport

import "bytes"

// SSEEventData splits one SSE event into its data-line payloads, with the
// "data:" prefix and one optional leading space stripped. commentOnly
// reports whether every line was a comment or blank.
func SSEEventData(event []byte) (data [][]byte, commentOnly bool) {
	commentOnly = true
	for _, line := range bytes.Split(event, []byte("\n")) {
		if rest, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			data = append(data, bytes.TrimPrefix(rest, []byte(" ")))
			commentOnly = false
		} else if len(line) > 0 && line[0] != ':' {
			commentOnly = false
		}
	}
	return data, commentOnly
}
