package idmap

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

type inMemoryMap struct {
	mu      sync.Mutex
	entries map[string]Entry
}

func newInMemoryMap() *inMemoryMap {
	return &inMemoryMap{entries: make(map[string]Entry)}
}

func (m *inMemoryMap) Store(_ context.Context, backendID any, serverName string, sessionID string, gatewaySessionID string) (string, error) {
	id := uuid.NewString()

	m.mu.Lock()
	defer m.mu.Unlock()

	m.entries[id] = Entry{
		BackendID:        backendID,
		ServerName:       serverName,
		SessionID:        sessionID,
		GatewaySessionID: gatewaySessionID,
	}

	return id, nil
}

func (m *inMemoryMap) Lookup(_ context.Context, gatewayID string) (Entry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, ok := m.entries[gatewayID]
	if !ok {
		return Entry{}, false, nil
	}

	return entry, ok, nil
}

func (m *inMemoryMap) Remove(_ context.Context, gatewayID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.entries, gatewayID)
}
