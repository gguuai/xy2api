package scheduling

import (
	"context"
	"database/sql"
	"errors"
)

// Mode is a system-wide choice. A request snapshots it once, independently of
// group policy and of the legacy settings document.
type Mode string

const (
	ModeSub2API    Mode = "sub2api"
	ModeControlled Mode = "controlled"
)

type ModeSnapshot struct {
	Mode    Mode  `json:"mode"`
	Version int64 `json:"version"`
}

func (m Mode) Valid() bool { return m == ModeSub2API || m == ModeControlled }

type ModeStore interface {
	GetSchedulingMode(context.Context) (ModeSnapshot, error)
	PutSchedulingMode(context.Context, Mode, int64) (ModeSnapshot, error)
}

func (s *PostgresStore) GetSchedulingMode(ctx context.Context) (ModeSnapshot, error) {
	result := ModeSnapshot{Mode: ModeControlled}
	if err := s.ready(); err != nil {
		return result, err
	}
	err := s.db.QueryRowContext(ctx, "SELECT mode,version FROM scheduling_system_mode WHERE singleton=TRUE").Scan(&result.Mode, &result.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return ModeSnapshot{Mode: ModeControlled}, nil
	}
	if err != nil {
		return ModeSnapshot{}, err
	}
	if !result.Mode.Valid() {
		return ModeSnapshot{}, ErrSharedState
	}
	return result, nil
}
func (s *PostgresStore) PutSchedulingMode(ctx context.Context, mode Mode, expected int64) (ModeSnapshot, error) {
	if !mode.Valid() || expected < 0 {
		return ModeSnapshot{}, ErrInvalidControl
	}
	if err := s.ready(); err != nil {
		return ModeSnapshot{}, err
	}
	var result ModeSnapshot
	var err error
	if expected == 0 {
		err = s.db.QueryRowContext(ctx, "INSERT INTO scheduling_system_mode(singleton,mode,version) VALUES(TRUE,$1,1) ON CONFLICT(singleton) DO NOTHING RETURNING mode,version", mode).Scan(&result.Mode, &result.Version)
	} else {
		err = s.db.QueryRowContext(ctx, "UPDATE scheduling_system_mode SET mode=$1,version=version+1,updated_at=NOW() WHERE singleton=TRUE AND version=$2 RETURNING mode,version", mode, expected).Scan(&result.Mode, &result.Version)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ModeSnapshot{}, ErrVersionConflict
	}
	return result, err
}
