package elicitation

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

func newTestRedisMap(t *testing.T, ttl time.Duration) (*redisMap, *miniredis.Miniredis) {
	t.Helper()
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return newRedisMap(client, ttl), srv
}

func TestRedisMap_StoreAndLookup(t *testing.T) {
	m, srv := newTestRedisMap(t, 5*time.Minute)
	ctx := context.Background()

	id, err := m.Store(ctx, "sess1", "github", "user123")
	if err != nil {
		t.Fatal(err)
	}
	if !srv.Exists("tokenelicitation:" + id) {
		t.Fatal("expected key with tokenelicitation: prefix")
	}

	entry, ok, err := m.Lookup(ctx, id)
	if err != nil || !ok {
		t.Fatalf("expected hit, got ok=%v err=%v", ok, err)
	}
	if entry.SessionID != "sess1" || entry.ServerName != "github" || entry.Sub != "user123" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestRedisMap_RemoveThenLookup(t *testing.T) {
	m, _ := newTestRedisMap(t, 5*time.Minute)
	ctx := context.Background()

	id, err := m.Store(ctx, "sess1", "github", "")
	if err != nil {
		t.Fatal(err)
	}
	m.Remove(ctx, id)

	_, ok, err := m.Lookup(ctx, id)
	if err != nil || ok {
		t.Fatalf("expected miss after remove, got ok=%v err=%v", ok, err)
	}
}

func TestRedisMap_ClaimIsSingleUse(t *testing.T) {
	m, _ := newTestRedisMap(t, 5*time.Minute)
	ctx := context.Background()

	id, err := m.Store(ctx, "sess1", "github", "user123")
	if err != nil {
		t.Fatal(err)
	}

	entry, ok, err := m.Claim(ctx, id)
	if err != nil || !ok {
		t.Fatalf("first claim: expected hit, got ok=%v err=%v", ok, err)
	}
	if entry.SessionID != "sess1" {
		t.Fatalf("unexpected entry: %+v", entry)
	}

	_, ok, err = m.Claim(ctx, id)
	if err != nil || ok {
		t.Fatalf("second claim: expected miss, got ok=%v err=%v", ok, err)
	}
}

func TestRedisMap_ConcurrentClaim(t *testing.T) {
	m, _ := newTestRedisMap(t, 5*time.Minute)
	ctx := context.Background()

	id, err := m.Store(ctx, "sess1", "github", "user123")
	if err != nil {
		t.Fatal(err)
	}

	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			_, ok, err := m.Claim(ctx, id)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if ok {
				wins.Add(1)
			}
		})
	}
	wg.Wait()

	if got := wins.Load(); got != 1 {
		t.Fatalf("expected exactly 1 winner, got %d", got)
	}
}

func TestRedisMap_ExpiredEntry(t *testing.T) {
	m, srv := newTestRedisMap(t, time.Minute)
	ctx := context.Background()

	id, err := m.Store(ctx, "sess1", "github", "user123")
	if err != nil {
		t.Fatal(err)
	}
	srv.FastForward(time.Minute + time.Second)

	if _, ok, err := m.Lookup(ctx, id); err != nil || ok {
		t.Fatalf("lookup: expected expired miss, got ok=%v err=%v", ok, err)
	}
	if _, ok, err := m.Claim(ctx, id); err != nil || ok {
		t.Fatalf("claim: expected expired miss, got ok=%v err=%v", ok, err)
	}
}

func TestRedisMap_CorruptValue(t *testing.T) {
	m, srv := newTestRedisMap(t, 5*time.Minute)
	ctx := context.Background()

	if err := srv.Set("tokenelicitation:bad", "not-json"); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := m.Lookup(ctx, "bad"); err == nil || ok {
		t.Fatalf("lookup: expected decode error, got ok=%v err=%v", ok, err)
	}
	if _, ok, err := m.Claim(ctx, "bad"); err == nil || ok {
		t.Fatalf("claim: expected decode error, got ok=%v err=%v", ok, err)
	}
}
