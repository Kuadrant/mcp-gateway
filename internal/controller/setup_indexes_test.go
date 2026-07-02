package controller

import (
	"context"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type mockFieldIndexer struct {
	calls []string
}

func (m *mockFieldIndexer) IndexField(_ context.Context, _ client.Object, field string, _ client.IndexerFunc) error {
	m.calls = append(m.calls, field)
	return nil
}

func TestSetupRequiredIndexes(t *testing.T) {
	indexer := &mockFieldIndexer{}
	if err := SetupRequiredIndexes(context.Background(), indexer); err != nil {
		t.Fatalf("SetupRequiredIndexes failed: %v", err)
	}
	if len(indexer.calls) != 2 {
		t.Fatalf("expected 2 index registrations, got %d", len(indexer.calls))
	}
	if indexer.calls[0] != gatewayIndexKey {
		t.Errorf("expected first index to be %q, got %q", gatewayIndexKey, indexer.calls[0])
	}
	if indexer.calls[1] != refGrantIndexKey {
		t.Errorf("expected second index to be %q, got %q", refGrantIndexKey, indexer.calls[1])
	}
}
