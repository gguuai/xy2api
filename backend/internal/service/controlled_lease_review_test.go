//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

// Blocking the database poll must not postpone cancellation of the actual HTTP
// transport at the local lease deadline. Only a synthetic local server is used.
func TestControlledLeaseReviewCancelsHTTPDuringBlockedStorePoll(t *testing.T) {
	s, db, _, _ := controlledIntegration(t, false)
	lock, err := db.Begin()
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Rollback() })
	_, err = lock.Exec("LOCK TABLE scheduling_attempts IN ACCESS EXCLUSIVE MODE")
	require.NoError(t, err)
	reached := make(chan struct{})
	upstreamCancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(reached)
		<-r.Context().Done()
		close(upstreamCancelled)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	transportResult := make(chan error, 1)
	go func() {
		response, e := server.Client().Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		transportResult <- e
	}()
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("synthetic HTTP request did not start")
	}
	d := &controlledDispatch{service: s, ticket: scheduling.DispatchTicket{TicketID: "lease-review", LeaseUntil: time.Now().Add(2 * time.Second)}, cancel: cancel, done: make(chan struct{})}
	exited := make(chan struct{})
	go func() { d.maintainLease(); close(exited) }()
	require.Eventually(t, func() bool {
		var waiting bool
		e := db.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='scheduling_attempts'::regclass AND NOT granted)").Scan(&waiting)
		return e == nil && waiting
	}, 1800*time.Millisecond, 20*time.Millisecond, "watchdog control poll must really be blocked on this fixture table")
	select {
	case transportErr := <-transportResult:
		require.ErrorIs(t, transportErr, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("lease expiry waited for the blocked database poll instead of cancelling HTTP")
	}
	select {
	case <-upstreamCancelled:
	case <-time.After(time.Second):
		t.Fatal("local HTTP server did not observe transport cancellation")
	}
	require.NoError(t, lock.Rollback())
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("expired watchdog did not exit after poll unblocked")
	}
	t.Log("actual local HTTP cancelled at lease deadline while PostgreSQL poll remained locked; watchdog exited after unlock")
}
