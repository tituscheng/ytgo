package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerPoolLifecycle(t *testing.T) {
	ctx := context.Background()
	wp := NewWorkerPool(2)

	// Submit before Start should error
	err := wp.Submit(ctx, func() error { return nil })
	assert.Error(t, err)

	wp.Start(ctx)

	// Submit after Start should succeed
	err = wp.Submit(ctx, func() error { return nil })
	require.NoError(t, err)

	err = wp.Wait()
	require.NoError(t, err)

	// Submit after Wait should error
	err = wp.Submit(ctx, func() error { return nil })
	assert.Error(t, err)
}

func TestWorkerPoolUnlimited(t *testing.T) {
	ctx := context.Background()
	wp := NewWorkerPool(0)
	wp.Start(ctx)

	var done bool
	err := wp.Submit(ctx, func() error {
		done = true
		return nil
	})
	require.NoError(t, err)

	err = wp.Wait()
	require.NoError(t, err)
	assert.True(t, done)
}
