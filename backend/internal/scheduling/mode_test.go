package scheduling

import (
	"context"
	"database/sql"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSchedulingModeDefaultsAndStorageFailures(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	s := NewPostgresStore(db)
	m.ExpectQuery("SELECT mode,version").WillReturnError(sql.ErrNoRows)
	got, err := s.GetSchedulingMode(context.Background())
	require.NoError(t, err)
	require.Equal(t, ModeSnapshot{Mode: ModeControlled}, got)
	m.ExpectQuery("SELECT mode,version").WillReturnError(errors.New("offline"))
	_, err = s.GetSchedulingMode(context.Background())
	require.Error(t, err)
	m.ExpectQuery("SELECT mode,version").WillReturnRows(sqlmock.NewRows([]string{"mode", "version"}).AddRow("corrupt", 1))
	_, err = s.GetSchedulingMode(context.Background())
	require.ErrorIs(t, err, ErrSharedState)
	require.NoError(t, m.ExpectationsWereMet())
}
func TestSchedulingModeCompareAndSwap(t *testing.T) {
	db, m, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	s := NewPostgresStore(db)
	ctx := context.Background()
	m.ExpectQuery("INSERT INTO scheduling_system_mode").WithArgs(ModeSub2API).WillReturnRows(sqlmock.NewRows([]string{"mode", "version"}).AddRow("sub2api", 1))
	got, err := s.PutSchedulingMode(ctx, ModeSub2API, 0)
	require.NoError(t, err)
	require.Equal(t, ModeSnapshot{Mode: ModeSub2API, Version: 1}, got)
	m.ExpectQuery("INSERT INTO scheduling_system_mode").WithArgs(ModeControlled).WillReturnError(sql.ErrNoRows)
	_, err = s.PutSchedulingMode(ctx, ModeControlled, 0)
	require.ErrorIs(t, err, ErrVersionConflict)
	m.ExpectQuery("UPDATE scheduling_system_mode").WithArgs(ModeControlled, int64(1)).WillReturnRows(sqlmock.NewRows([]string{"mode", "version"}).AddRow("controlled", 2))
	got, err = s.PutSchedulingMode(ctx, ModeControlled, 1)
	require.NoError(t, err)
	require.EqualValues(t, 2, got.Version)
	m.ExpectQuery("UPDATE scheduling_system_mode").WithArgs(ModeSub2API, int64(1)).WillReturnError(sql.ErrNoRows)
	_, err = s.PutSchedulingMode(ctx, ModeSub2API, 1)
	require.ErrorIs(t, err, ErrVersionConflict)
	_, err = s.PutSchedulingMode(ctx, "legacy", 2)
	require.ErrorIs(t, err, ErrInvalidControl)
	_, err = s.PutSchedulingMode(ctx, ModeControlled, -1)
	require.ErrorIs(t, err, ErrInvalidControl)
	require.NoError(t, m.ExpectationsWereMet())
}
