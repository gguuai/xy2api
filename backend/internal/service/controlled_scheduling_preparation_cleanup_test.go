package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestControlledPreparationCleanupExpiredReturnsBeforeDetachedOperation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	begin := time.Now()
	done := controlledCompensate(ctx, "test cleanup", func(c context.Context) error { started <- c; <-release; return nil })
	require.Less(t, time.Since(begin), 100*time.Millisecond)
	background := <-started
	require.NoError(t, background.Err())
	deadline, ok := background.Deadline()
	require.True(t, ok)
	require.InDelta(t, 5, time.Until(deadline).Seconds(), 0.5)
	select {
	case <-done:
		t.Fatal("cleanup completed before the operation returned")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("finite cleanup did not terminate")
	}
}

func TestControlledPreparationCleanupDeadlineRetriesOnceAndTerminates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var calls atomic.Int32
	secondStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	begin := time.Now()
	done := controlledCompensate(ctx, "test cleanup", func(c context.Context) error {
		if calls.Add(1) == 1 {
			<-c.Done()
			return c.Err()
		}
		secondStarted <- struct{}{}
		<-release
		return nil
	})
	require.Less(t, time.Since(begin), 200*time.Millisecond)
	<-secondStarted
	require.Equal(t, int32(2), calls.Load())
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bounded retry did not terminate")
	}
	require.Equal(t, int32(2), calls.Load())
}

func TestControlledPreparationCleanupSuccessDoesNotStartWorker(t *testing.T) {
	var calls atomic.Int32
	done := controlledCompensate(context.Background(), "test cleanup", func(c context.Context) error {
		calls.Add(1)
		deadline, ok := c.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), 5*time.Second)
		return nil
	})
	select {
	case <-done:
	default:
		t.Fatal("synchronous success left a pending worker")
	}
	require.Equal(t, int32(1), calls.Load())
}
