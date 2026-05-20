package elicitation

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestInMemoryMap_StoreAndLookup(t *testing.T) {
	m := newInMemoryMap(5 * time.Minute)
	ctx := context.Background()

	id, err := m.Store(ctx, "sess1", "github", "user123")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected non-empty elicitation ID")
	}

	entry, ok, err := m.Lookup(ctx, id)
	if err != nil || !ok {
		t.Fatalf("expected hit, got ok=%v err=%v", ok, err)
	}
	if entry.SessionID != "sess1" || entry.ServerName != "github" || entry.Sub != "user123" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestInMemoryMap_LookupMissing(t *testing.T) {
	m := newInMemoryMap(5 * time.Minute)
	_, ok, err := m.Lookup(context.Background(), "nonexistent")
	if err != nil || ok {
		t.Fatalf("expected miss, got ok=%v err=%v", ok, err)
	}
}

func TestInMemoryMap_RemoveThenLookup(t *testing.T) {
	m := newInMemoryMap(5 * time.Minute)
	ctx := context.Background()

	id, _ := m.Store(ctx, "sess1", "github", "")
	m.Remove(ctx, id)

	_, ok, _ := m.Lookup(ctx, id)
	if ok {
		t.Fatal("expected miss after remove")
	}
}

func TestInMemoryMap_ExpiredEntry(t *testing.T) {
	m := newInMemoryMap(1 * time.Millisecond)
	ctx := context.Background()

	id, _ := m.Store(ctx, "sess1", "github", "user123")
	time.Sleep(5 * time.Millisecond)

	_, ok, err := m.Lookup(ctx, id)
	if err != nil || ok {
		t.Fatalf("expected expired miss, got ok=%v err=%v", ok, err)
	}
}

func TestInMemoryMap_ClaimIsAtomic(t *testing.T) {
	m := newInMemoryMap(5 * time.Minute)
	ctx := context.Background()

	id, _ := m.Store(ctx, "sess1", "github", "user123")

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

	_, ok, _ = m.Lookup(ctx, id)
	if ok {
		t.Fatal("entry should not exist after claim")
	}
}

func TestInMemoryMap_ClaimExpired(t *testing.T) {
	m := newInMemoryMap(1 * time.Millisecond)
	ctx := context.Background()

	id, _ := m.Store(ctx, "sess1", "github", "")
	time.Sleep(5 * time.Millisecond)

	_, ok, err := m.Claim(ctx, id)
	if err != nil || ok {
		t.Fatalf("expected expired miss, got ok=%v err=%v", ok, err)
	}
}

func TestInMemoryMap_ConcurrentClaim(t *testing.T) {
	m := newInMemoryMap(5 * time.Minute)
	ctx := context.Background()

	id, _ := m.Store(ctx, "sess1", "github", "user123")

	const n = 20
	wins := make(chan bool, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			_, ok, err := m.Claim(ctx, id)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			wins <- ok
		}()
	}
	wg.Wait()
	close(wins)

	var winCount int
	for ok := range wins {
		if ok {
			winCount++
		}
	}
	if winCount != 1 {
		t.Fatalf("expected exactly 1 winner, got %d", winCount)
	}
}

func TestInMemoryMap_EmptySub(t *testing.T) {
	m := newInMemoryMap(5 * time.Minute)
	ctx := context.Background()

	id, _ := m.Store(ctx, "sess1", "github", "")

	entry, ok, err := m.Lookup(ctx, id)
	if err != nil || !ok {
		t.Fatalf("expected hit, got ok=%v err=%v", ok, err)
	}
	if entry.Sub != "" {
		t.Fatalf("expected empty sub, got %q", entry.Sub)
	}
}
