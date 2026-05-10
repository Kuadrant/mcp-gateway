package broker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscoveryScopeStoreGetReturnsDefensiveCopy(t *testing.T) {
	s := newDiscoveryScopeStore(1000, 0)
	sel := map[string]struct{}{"a": {}, "b": {}}
	s.Set("sess1", discoveryScopeFiltered, sel)

	_, got1, ok := s.Get("sess1")
	require.True(t, ok)
	delete(got1, "a")

	_, got2, ok := s.Get("sess1")
	require.True(t, ok)
	_, still := got2["a"]
	require.True(t, still)
}
