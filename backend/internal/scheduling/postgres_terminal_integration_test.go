package scheduling

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This deliberately uses PostgreSQL, not sqlmock: polymorphic JSON argument
// inference and transaction rollback are part of the contract being tested.
func TestPostgresTerminalIntentCompensatesFailedSettlement(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	ticket, err := s.BeginDispatch(ctx, testDispatch(1, ""))
	require.NoError(t, err)
	require.NoError(t, s.RecordTerminalIntent(ctx, ticket.TicketID, "completed", "remote_terminal", true))
	var raw []byte
	require.NoError(t, db.QueryRowContext(ctx, "SELECT metrics FROM scheduling_attempts WHERE ticket_id=$1", ticket.TicketID).Scan(&raw))
	var metrics map[string]any
	require.NoError(t, json.Unmarshal(raw, &metrics))
	require.Equal(t, true, metrics["terminal_intent"])
	require.Equal(t, "completed", metrics["terminal_outcome"])
	require.Equal(t, "remote_terminal", metrics["terminal_certainty"])
	require.Equal(t, true, metrics["terminal_usage_pending"])

	// Sequence advancement survives rollback, so the injected transaction fails
	// exactly once without changing or mocking the settlement implementation.
	_, err = db.ExecContext(ctx, `
        CREATE SEQUENCE terminal_settlement_fault;
        CREATE FUNCTION fail_first_terminal_settlement() RETURNS trigger AS $$
        BEGIN
            IF NEW.state='settled' AND OLD.state<>'settled'
               AND nextval('terminal_settlement_fault')=1 THEN
                RAISE EXCEPTION 'injected first settlement failure';
            END IF;
            RETURN NEW;
        END; $$ LANGUAGE plpgsql;
        CREATE TRIGGER terminal_settlement_fault BEFORE UPDATE ON scheduling_attempts
        FOR EACH ROW EXECUTE FUNCTION fail_first_terminal_settlement();
    `)
	require.NoError(t, err)
	err = s.SettleAttempt(ctx, ticket.TicketID, "completed", true)
	require.ErrorContains(t, err, "injected first settlement failure")
	counts, err := s.AccountActiveCounts(ctx, []int64{1})
	require.NoError(t, err)
	require.Equal(t, 1, counts[1], "failed transaction must not release capacity")
	require.NoError(t, s.MarkAttemptUnknown(ctx, ticket.TicketID))
	settled, err := s.ReconcileTerminalIntents(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, settled)
	counts, err = s.AccountActiveCounts(ctx, []int64{1})
	require.NoError(t, err)
	require.Zero(t, counts[1])
	var outcome string
	var at time.Time
	require.NoError(t, db.QueryRowContext(ctx, "SELECT outcome,settled_at FROM scheduling_attempts WHERE ticket_id=$1", ticket.TicketID).Scan(&outcome, &at))
	require.Equal(t, "completed", outcome)
	require.NoError(t, s.SettleAttempt(ctx, ticket.TicketID, "cancelled", false))
	settled, err = s.ReconcileTerminalIntents(ctx)
	require.NoError(t, err)
	require.Zero(t, settled)
	var after time.Time
	require.NoError(t, db.QueryRowContext(ctx, "SELECT outcome,settled_at FROM scheduling_attempts WHERE ticket_id=$1", ticket.TicketID).Scan(&outcome, &after))
	require.Equal(t, "completed", outcome)
	require.Equal(t, at, after)
	require.NoError(t, s.AcknowledgeUsage(ctx, ticket.TicketID))
	var pending, acknowledged bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT usage_pending,usage_acknowledged FROM scheduling_attempts WHERE ticket_id=$1", ticket.TicketID).Scan(&pending, &acknowledged))
	require.False(t, pending)
	require.True(t, acknowledged)
}

func TestPostgresTerminalIntentCancelledWriteKeepsUnknownCapacity(t *testing.T) {
	s, db := isolatedControlStore(t)
	ctx := context.Background()
	ticket, err := s.BeginDispatch(ctx, testDispatch(1, ""))
	require.NoError(t, err)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, s.RecordTerminalIntent(cancelled, ticket.TicketID, "completed", "remote_terminal", false), context.Canceled)
	require.NoError(t, s.MarkAttemptUnknown(ctx, ticket.TicketID))
	n, err := s.ReconcileTerminalIntents(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
	counts, err := s.AccountActiveCounts(ctx, []int64{1})
	require.NoError(t, err)
	require.Equal(t, 1, counts[1])
	var state string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT state FROM scheduling_attempts WHERE ticket_id=$1", ticket.TicketID).Scan(&state))
	require.Equal(t, "unknown", state)
}
