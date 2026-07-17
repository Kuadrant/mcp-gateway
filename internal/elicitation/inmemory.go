// Package elicitation manages elicitation state for URL-based token collection.
package elicitation

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

type inMemoryEntry struct {
	entry     Entry
	expiresAt time.Time
}

type inMemoryMap struct {
	mu       sync.Mutex
	entries  map[string]inMemoryEntry
	entryTTL time.Duration
	stopCh   chan struct{}
}

func newInMemoryMap(entryTTL time.Duration) *inMemoryMap {
	m := &inMemoryMap{
		entries:  make(map[string]inMemoryEntry),
		entryTTL: entryTTL,
		stopCh:   make(chan struct{}),
	}
	go m.reapLoop()
	return m
}

func (m *inMemoryMap) reapLoop() {
	ticker := time.NewTicker(m.entryTTL)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case now := <-ticker.C:
			m.mu.Lock()
			for id, e := range m.entries {
				if now.After(e.expiresAt) {
					delete(m.entries, id)
				}
			}
			m.mu.Unlock()
		}
	}
}

func (m *inMemoryMap) Close() {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
}

func (m *inMemoryMap) Store(_ context.Context, sessionID, serverName, sub string) (string, error) {
	id := uuid.NewString()

	m.mu.Lock()
	defer m.mu.Unlock()

	m.entries[id] = inMemoryEntry{
		entry: Entry{
			SessionID:  sessionID,
			ServerName: serverName,
			Sub:        sub,
		},
		expiresAt: time.Now().Add(m.entryTTL),
	}

	return id, nil
}

func (m *inMemoryMap) Lookup(_ context.Context, elicitationID string) (Entry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.entries[elicitationID]
	if !ok {
		return Entry{}, false, nil
	}
	if time.Now().After(e.expiresAt) {
		delete(m.entries, elicitationID)
		return Entry{}, false, nil
	}
	return e.entry, true, nil
}

func (m *inMemoryMap) Claim(_ context.Context, elicitationID string) (Entry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	e, ok := m.entries[elicitationID]
	if !ok {
		return Entry{}, false, nil
	}
	delete(m.entries, elicitationID)
	if time.Now().After(e.expiresAt) {
		return Entry{}, false, nil
	}
	return e.entry, true, nil
}

func (m *inMemoryMap) Remove(_ context.Context, elicitationID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, elicitationID)
}
