package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/Kuadrant/mcp-gateway/internal/transport"
)

// discoverCapture wraps an http.RoundTripper and intercepts the SDK's
// server/discover response to capture the upstream's SupportedVersions.
// the captured versions are read after Connect via Versions().
type discoverCapture struct {
	base      http.RoundTripper
	mu        sync.Mutex
	versions  []string
	connected bool
}

func (d *discoverCapture) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || !isDiscoverRequest(req) {
		return d.base.RoundTrip(req)
	}
	resp, err := d.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &discoverTeeBody{
		rc:      resp.Body,
		sse:     strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"),
		capture: d,
	}
	return resp, nil
}

// SetConnected marks the capture as having completed a connection attempt.
// must be called after client.Connect returns.
func (d *discoverCapture) SetConnected() {
	d.mu.Lock()
	d.connected = true
	d.mu.Unlock()
}

// Versions returns the captured SupportedVersions. returns an error if
// called before Connect has completed.
func (d *discoverCapture) Versions() ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.connected {
		return nil, fmt.Errorf("versions called before connect")
	}
	return d.versions, nil
}

func (d *discoverCapture) store(versions []string) {
	d.mu.Lock()
	d.versions = versions
	d.mu.Unlock()
}

// isDiscoverRequest is only used for the broker client. gateway client requests don't go through this client.
// so reading the body here while not ideal is not high cost or an unknown it will either be a init/discover or a list request
func isDiscoverRequest(req *http.Request) bool {
	body, ok := transport.PeekRequestBody(req)
	if !ok {
		return false
	}
	var env struct {
		Method string `json:"method"`
	}
	return json.Unmarshal(body, &env) == nil && env.Method == "server/discover"
}

const maxDiscoverResponseBytes = 64 * 1024

// discoverTeeBody accumulates the discover response while the SDK reads it,
// then extracts SupportedVersions on EOF/Close.
type discoverTeeBody struct {
	rc      io.ReadCloser
	buf     bytes.Buffer
	sse     bool
	capture *discoverCapture
	done    bool
}

func (b *discoverTeeBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 && b.buf.Len() < maxDiscoverResponseBytes {
		remaining := min(n, maxDiscoverResponseBytes-b.buf.Len())
		b.buf.Write(p[:remaining])
	}
	if err == io.EOF {
		b.harvest()
	}
	return n, err
}

func (b *discoverTeeBody) Close() error {
	b.harvest()
	return b.rc.Close()
}

func (b *discoverTeeBody) harvest() {
	if b.done {
		return
	}
	b.done = true
	if b.sse {
		for _, payload := range ssePayloads(b.buf.Bytes()) {
			if versions := parseDiscoverVersions(payload); len(versions) > 0 {
				b.capture.store(versions)
				return
			}
		}
		return
	}
	if versions := parseDiscoverVersions(b.buf.Bytes()); len(versions) > 0 {
		b.capture.store(versions)
	}
}

func parseDiscoverVersions(payload []byte) []string {
	var envelope struct {
		Result struct {
			SupportedVersions []string `json:"supportedVersions"`
		} `json:"result"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return nil
	}
	return envelope.Result.SupportedVersions
}
