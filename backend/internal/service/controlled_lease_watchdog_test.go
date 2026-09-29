//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestControlledExpiredLeaseCancelsTransportWithoutRenewalResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &controlledDispatch{ticket: scheduling.DispatchTicket{LeaseUntil: time.Now().Add(25 * time.Millisecond)}, cancel: cancel, done: make(chan struct{})}
	exited := make(chan struct{})
	go func() { d.maintainLease(); close(exited) }()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("expired local admission must cancel the outstanding upstream transport")
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("expired holder must stop polling and cannot renew itself")
	}
}

func TestControlledFinishedLeaseStopsWatchdog(t *testing.T) {
	called := make(chan struct{}, 1)
	d := &controlledDispatch{ticket: scheduling.DispatchTicket{LeaseUntil: time.Now().Add(50 * time.Millisecond)}, cancel: func() { called <- struct{}{} }, done: make(chan struct{})}
	close(d.done)
	d.maintainLease()
	select {
	case <-called:
		t.Fatal("completed attempt retained a delayed cancellation callback")
	case <-time.After(90 * time.Millisecond):
	}
	require.Empty(t, called)
}
