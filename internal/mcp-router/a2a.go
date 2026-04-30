package mcprouter

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
)

// a2aTaskExtractor reads A2A message/stream (and message/send) SSE response chunks,
// extracts the task ID from the first result event, and stores the taskID→serverName
// mapping in the session cache so that follow-on tasks/get, tasks/cancel, and
// tasks/resubscribe requests can be routed to the correct backend agent.
//
// The body is passed through unmodified; this extractor is read-only.
type a2aTaskExtractor struct {
	buf        []byte
	logger     *slog.Logger
	serverName string
	cache      SessionCache
	done       bool
}

type a2aResult struct {
	ID string `json:"id"`
}

type a2aEnvelope struct {
	Result *a2aResult `json:"result"`
}

// Process scans chunk for complete SSE data lines, extracts the first task ID,
// and stores the taskID→serverName mapping. Returns chunk unchanged.
// Call Flush at end-of-stream to handle plain-JSON bodies (message/send).
func (e *a2aTaskExtractor) Process(ctx context.Context, chunk []byte) []byte {
	if e.done {
		return chunk
	}
	e.buf = append(e.buf, chunk...)

	for {
		nl := bytes.IndexByte(e.buf, '\n')
		if nl == -1 {
			break // incomplete line — wait for more data
		}
		line := bytes.TrimSpace(e.buf[:nl+1])
		e.buf = e.buf[nl+1:]

		var jsonData []byte
		switch {
		case bytes.HasPrefix(line, dataPrefix):
			jsonData = bytes.TrimPrefix(line, dataPrefix)
			if len(jsonData) > 0 && jsonData[0] == ' ' {
				jsonData = jsonData[1:]
			}
		case len(line) > 0 && line[0] == '{':
			jsonData = line
		default:
			continue
		}

		if taskID := extractA2ATaskID(jsonData); taskID != "" {
			e.storeRoute(ctx, taskID)
			return chunk
		}
	}

	return chunk
}

// Flush handles plain-JSON response bodies (message/send) that may not carry a
// trailing newline. Call this when EndOfStream is true.
func (e *a2aTaskExtractor) Flush(ctx context.Context) {
	if e.done || len(e.buf) == 0 {
		return
	}
	candidate := bytes.TrimSpace(e.buf)
	e.buf = nil
	if len(candidate) > 0 && candidate[0] == '{' {
		if taskID := extractA2ATaskID(candidate); taskID != "" {
			e.storeRoute(ctx, taskID)
		}
	}
}

func (e *a2aTaskExtractor) storeRoute(ctx context.Context, taskID string) {
	if err := e.cache.StoreTaskRoute(ctx, taskID, e.serverName); err != nil {
		e.logger.ErrorContext(ctx, "failed to store a2a task route", "taskID", taskID, "error", err)
	} else {
		e.logger.DebugContext(ctx, "stored a2a task route", "taskID", taskID, "server", e.serverName)
	}
	e.done = true
}

func extractA2ATaskID(data []byte) string {
	var env a2aEnvelope
	if err := json.Unmarshal(data, &env); err != nil || env.Result == nil {
		return ""
	}
	return env.Result.ID
}
