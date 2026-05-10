package broker

import (
	"maps"
	"sync"
	"time"
)

type discoveryScopeKind uint8

const (
	discoveryScopeUnset discoveryScopeKind = iota
	discoveryScopeAll
	discoveryScopeFiltered
)

type discoveryScopeEntry struct {
	kind     discoveryScopeKind
	selected map[string]struct{}
	updated  time.Time
}

// discoveryScopeStore holds per-session tool scope for progressive discovery.
// Evicts entries by TTL and bounds map size.
type discoveryScopeStore struct {
	mu         sync.Mutex
	entries    map[string]*discoveryScopeEntry
	maxEntries int
	ttl        time.Duration
}

func newDiscoveryScopeStore(maxEntries int, ttl time.Duration) *discoveryScopeStore {
	if maxEntries <= 0 {
		maxEntries = 100_000
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &discoveryScopeStore{
		entries:    make(map[string]*discoveryScopeEntry),
		maxEntries: maxEntries,
		ttl:        ttl,
	}
}

func (s *discoveryScopeStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(time.Now())
	return len(s.entries)
}

func (s *discoveryScopeStore) pruneLocked(now time.Time) {
	for id, e := range s.entries {
		if now.Sub(e.updated) > s.ttl {
			delete(s.entries, id)
		}
	}
	for len(s.entries) > s.maxEntries {
		var victim string
		var victimTime time.Time
		first := true
		for id, e := range s.entries {
			if first || e.updated.Before(victimTime) {
				first = false
				victim = id
				victimTime = e.updated
			}
		}
		if victim == "" {
			break
		}
		delete(s.entries, victim)
	}
}

// Set records scope for a session. selected is only used when kind is discoveryScopeFiltered.
func (s *discoveryScopeStore) Set(sessionID string, kind discoveryScopeKind, selected map[string]struct{}) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.pruneLocked(now)
	e := &discoveryScopeEntry{kind: kind, updated: now}
	if kind == discoveryScopeFiltered && len(selected) > 0 {
		e.selected = maps.Clone(selected)
	}
	s.entries[sessionID] = e
}

func (s *discoveryScopeStore) Delete(sessionID string) {
	if s == nil || sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, sessionID)
}

// Get returns scope kind and a defensive copy of selected (nil unless filtered). Missing or expired session yields unset.
func (s *discoveryScopeStore) Get(sessionID string) (discoveryScopeKind, map[string]struct{}, bool) {
	if s == nil || sessionID == "" {
		return discoveryScopeUnset, nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.pruneLocked(now)
	e, ok := s.entries[sessionID]
	if !ok {
		return discoveryScopeUnset, nil, false
	}
	if now.Sub(e.updated) > s.ttl {
		delete(s.entries, sessionID)
		return discoveryScopeUnset, nil, false
	}
	e.updated = now

	switch e.kind {
	case discoveryScopeFiltered:
		if len(e.selected) == 0 {
			return discoveryScopeFiltered, map[string]struct{}{}, true
		}
		return discoveryScopeFiltered, maps.Clone(e.selected), true
	case discoveryScopeAll:
		return discoveryScopeAll, nil, true
	default:
		delete(s.entries, sessionID)
		return discoveryScopeUnset, nil, false
	}
}
