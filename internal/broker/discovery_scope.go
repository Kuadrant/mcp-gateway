package broker

import (
	"sync"
	"time"
)

// scopeState represents the session's tool scope state
type scopeState int

const (
	// scopeUnset means no scope has been applied (show all tools)
	scopeUnset scopeState = iota
	// scopeAll means scope was explicitly reset to all tools
	scopeAll
	// scopeFiltered means a specific set of tools has been selected
	scopeFiltered
)

// sessionScope holds the scoping state for a single session
type sessionScope struct {
	state    scopeState
	tools    map[string]struct{}
	expireAt time.Time
}

// scopeStore is an in-memory store for session tool scopes with TTL eviction.
type scopeStore struct {
	mu        sync.RWMutex
	scopes    map[string]*sessionScope
	ttl       time.Duration
	maxSize   int
	done      chan struct{}
	closeOnce sync.Once
}

const scopeEvictInterval = 5 * time.Minute

// newScopeStore creates a scope store with the given TTL and max size.
// It starts a background goroutine for periodic eviction.
func newScopeStore(ttl time.Duration, maxSize int) *scopeStore {
	if ttl <= 0 {
		ttl = defaultScopeTTL
	}
	if maxSize <= 0 {
		maxSize = defaultScopeMaxSize
	}
	s := &scopeStore{
		scopes:  make(map[string]*sessionScope),
		ttl:     ttl,
		maxSize: maxSize,
		done:    make(chan struct{}),
	}
	go s.evictLoop()
	return s
}

func (s *scopeStore) evictLoop() {
	ticker := time.NewTicker(scopeEvictInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.evictExpired()
		case <-s.done:
			return
		}
	}
}

func (s *scopeStore) evictExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, sc := range s.scopes {
		if now.After(sc.expireAt) {
			delete(s.scopes, id)
		}
	}
}

// setScope stores a filtered scope for a session
func (s *scopeStore) setScope(sessionID string, tools []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// enforce size cap by evicting oldest if at limit
	if len(s.scopes) >= s.maxSize {
		s.evictOldestLocked()
	}

	toolSet := make(map[string]struct{}, len(tools))
	for _, t := range tools {
		toolSet[t] = struct{}{}
	}
	s.scopes[sessionID] = &sessionScope{
		state:    scopeFiltered,
		tools:    toolSet,
		expireAt: time.Now().Add(s.ttl),
	}
}

// resetScope sets the session scope to "all tools"
func (s *scopeStore) resetScope(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// enforce size cap by evicting oldest if at limit (and this is a new entry)
	if _, exists := s.scopes[sessionID]; !exists && len(s.scopes) >= s.maxSize {
		s.evictOldestLocked()
	}

	s.scopes[sessionID] = &sessionScope{
		state:    scopeAll,
		tools:    nil,
		expireAt: time.Now().Add(s.ttl),
	}
}

// getScope returns the scope state and a defensive copy of the tool set.
// Returns scopeUnset if the session has no scope entry.
func (s *scopeStore) getScope(sessionID string) (scopeState, map[string]struct{}) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sc, ok := s.scopes[sessionID]
	if !ok {
		return scopeUnset, nil
	}
	if time.Now().After(sc.expireAt) {
		return scopeUnset, nil
	}
	if sc.state != scopeFiltered {
		return sc.state, nil
	}
	// defensive copy
	cpy := make(map[string]struct{}, len(sc.tools))
	for k := range sc.tools {
		cpy[k] = struct{}{}
	}
	return sc.state, cpy
}

// deleteScope removes a session's scope (e.g. on disconnect)
func (s *scopeStore) deleteScope(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.scopes, sessionID)
}

// size returns the number of tracked sessions
func (s *scopeStore) size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.scopes)
}

// stop shuts down the eviction goroutine. safe to call multiple times.
func (s *scopeStore) stop() {
	s.closeOnce.Do(func() { close(s.done) })
}

// evictOldestLocked evicts the entry with the earliest expiry. caller must hold mu.
func (s *scopeStore) evictOldestLocked() {
	var oldestID string
	var oldestTime time.Time
	first := true
	for id, sc := range s.scopes {
		if first || sc.expireAt.Before(oldestTime) {
			oldestID = id
			oldestTime = sc.expireAt
			first = false
		}
	}
	if !first {
		delete(s.scopes, oldestID)
	}
}
