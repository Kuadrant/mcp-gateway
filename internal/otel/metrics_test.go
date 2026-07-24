package otel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewMetricsProvider(t *testing.T) {
	ctx := context.Background()
	config := NewConfig("abc123", "", "v0.0.1")

	mp, err := NewMetricsProvider(ctx, config)
	require.NoError(t, err)
	require.NotNil(t, mp)

	err = mp.Shutdown(ctx)
	require.NoError(t, err)
}

func TestMetricsProvider_HTTPHandler(t *testing.T) {
	ctx := context.Background()
	config := NewConfig("abc123", "", "v0.0.1")

	mp, err := NewMetricsProvider(ctx, config)
	require.NoError(t, err)
	require.NotNil(t, mp.HTTPHandler())

	_ = mp.Shutdown(ctx)
}
