package service

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

func TestControlledStopReasonPreservesAttemptOutcome(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithCancel(NewControlledRequestContext(context.Background(), "responses"))
	r := controlledRequest(ctx)
	r.currentAttemptID = "attempt-1"
	r.control = &ControlledSchedulingService{Store: scheduling.NewPostgresStore(db)}
	r.history = []SchedulingAttemptTrace{{AttemptID: "attempt-1", Outcome: "upstream_error", StopReason: "upstream_error"}}
	mock.ExpectExec("UPDATE scheduling_attempts").WithArgs("attempt-1", "{\"stop_reason\":\"scheduling_retry_budget_exhausted\"}").WillReturnResult(sqlmock.NewResult(0, 1))
	cancel()
	RecordControlledSchedulingStop(ctx, "scheduling_retry_budget_exhausted")
	require.NoError(t, mock.ExpectationsWereMet())
	require.Equal(t, "upstream_error", r.history[0].Outcome)
	require.Equal(t, "scheduling_retry_budget_exhausted", r.history[0].StopReason)
}
