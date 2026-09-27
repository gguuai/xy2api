package scheduling

import (
	"context"
	"database/sql"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"regexp"
	"testing"
	"time"
)

func TestPostgresPolicyMissingPreservesLegacy(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	m.ExpectQuery(regexp.QuoteMeta("SELECT version, policy FROM scheduling_policies WHERE group_id=$1 AND model=$2")).WithArgs(int64(7), "test-model").WillReturnError(sql.ErrNoRows)
	got, err := NewPostgresStore(db).GetPolicy(context.Background(), 7, "test-model")
	require.NoError(t, err)
	require.Nil(t, got.Policy)
	require.Zero(t, got.Version)
	require.NoError(t, m.ExpectationsWereMet())
}
func TestPostgresUnavailableFailsClosed(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	m.ExpectBegin().WillReturnError(errors.New("unavailable"))
	_, err = NewPostgresStore(db).BeginDispatch(context.Background(), DispatchRequest{RequestID: "r", AccountID: 1, NodeID: "n"})
	require.Error(t, err)
	require.NoError(t, m.ExpectationsWereMet())
	_, err = NewPostgresStore(nil).Control(context.Background(), ControlCommand{AccountID: 1, Action: "pause"})
	require.ErrorIs(t, err, ErrSharedState)
}
func TestPostgresInvalidControlDoesNotWrite(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	_, err = NewPostgresStore(db).Control(context.Background(), ControlCommand{AccountID: 1, Action: "session_drain", SessionDurationSeconds: 0, SessionMaxTurns: 1})
	require.ErrorIs(t, err, ErrInvalidControl)
	require.NoError(t, m.ExpectationsWereMet())
}
func TestForceStopEpochFencesDelayedCancellation(t *testing.T) {
	c := ControlSnapshot{SubjectID: 1, Scope: ScopeAccount, State: ControlDraining, Mode: "force_stop", Epoch: 4}
	require.True(t, ForceStopApplies(c, DispatchTicket{AccountID: 1, AccountEpoch: 3}))
	require.False(t, ForceStopApplies(c, DispatchTicket{AccountID: 1, AccountEpoch: 5}))
	require.False(t, ForceStopApplies(c, DispatchTicket{AccountID: 2, AccountEpoch: 0}))
	c.Scope = ScopeFamily
	require.True(t, ForceStopApplies(c, DispatchTicket{AccountID: 2, FamilyID: 1, FamilyEpoch: 3}))
	c.State = ControlRunning
	require.False(t, ForceStopApplies(c, DispatchTicket{FamilyID: 1, FamilyEpoch: 0}))
}
func TestPostgresRenewCannotResurrectExpiredAttempt(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	m.ExpectExec("UPDATE scheduling_attempts SET lease_until").WithArgs("expired", int64(30000)).WillReturnResult(sqlmock.NewResult(0, 0))
	require.ErrorIs(t, NewPostgresStore(db).RenewAttempt(context.Background(), "expired", 30*time.Second), ErrAttemptIdentity)
	require.NoError(t, m.ExpectationsWereMet())
}

func TestPostgresRecordTerminalIntentIsDurableBeforeSettlement(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	m.ExpectExec("UPDATE scheduling_attempts").WithArgs("ticket-1", "completed", "remote_terminal", true).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, NewPostgresStore(db).RecordTerminalIntent(context.Background(), "ticket-1", "completed", "remote_terminal", true))
	require.NoError(t, m.ExpectationsWereMet())
}

func TestPostgresReconcileTerminalIntentsReadsOnlyValidPendingRows(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	m.ExpectQuery("SELECT ticket_id").WillReturnRows(sqlmock.NewRows([]string{"ticket_id", "terminal_outcome", "terminal_usage_pending"}))
	settled, err := NewPostgresStore(db).ReconcileTerminalIntents(context.Background())
	require.NoError(t, err)
	require.Zero(t, settled)
	require.NoError(t, m.ExpectationsWereMet())
}
