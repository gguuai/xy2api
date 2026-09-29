package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/liulixin-lex/xy2api/ent"
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOneClickSchedulingStaleAdminEditPreservesLockedSwitch(t *testing.T) {
	for _, stored := range []bool{false, true} {
		for _, adminEdit := range []bool{false, true} {
			name := "explicit_update"
			if adminEdit {
				name = "ordinary_admin_edit"
			}
			t.Run(name+map[bool]string{true: "_on", false: "_off"}[stored], func(t *testing.T) {
				db, mock, err := sqlmock.New()
				require.NoError(t, err)
				t.Cleanup(func() { _ = db.Close() })
				client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
				stopped := errors.New("observed mutation before SQL write")
				observed := false
				client.Account.Use(func(next dbent.Mutator) dbent.Mutator {
					return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
						mutation, ok := m.(*dbent.AccountMutation)
						require.True(t, ok)
						value, set := mutation.Schedulable()
						require.True(t, set)
						want := !stored
						if adminEdit {
							want = stored
						}
						require.Equal(t, want, value)
						observed = true
						return nil, stopped
					})
				})
				mock.ExpectBegin()
				mock.ExpectQuery("(?s)"+regexp.QuoteMeta("SELECT")+".*"+regexp.QuoteMeta("FOR NO KEY UPDATE")).WithArgs(int64(27), service.PlatformOpenAI, service.AccountTypeAPIKey, "{\"api_key\":\"sk-test\"}", nil).WillReturnRows(sqlmock.NewRows([]string{"identity_unchanged", "ollama_group_unchanged", "ollama_proxy_unchanged", "enabled", "rate_sync_enabled", "snapshot", "ollama_session", "ollama_auto", "ollama_snapshot", "opencode_group_unchanged", "opencode_auto", "opencode_snapshot", "current_extra"}).AddRow(true, false, true, nil, nil, nil, nil, nil, nil, false, nil, nil, nil))
				mock.ExpectQuery("(?s)SELECT .* FROM \"accounts\".*FOR UPDATE").WithArgs(int64(27)).WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "type", "credentials", "extra", "schedulable"}).AddRow(27, service.PlatformOpenAI, service.AccountTypeAPIKey, "{\"api_key\":\"sk-test\"}", "{}", stored))
				mock.ExpectRollback()
				repo := newAccountRepositoryWithSQL(client, db, nil)
				stale := &service.Account{ID: 27, Name: "renamed", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-test"}, Status: service.StatusActive, Schedulable: !stored, Concurrency: 1, Priority: 1}
				if adminEdit {
					err = repo.UpdateWithAccountBillingSettings(context.Background(), stale, nil, nil, nil)
				} else {
					err = repo.Update(context.Background(), stale)
				}
				require.ErrorIs(t, err, stopped)
				require.True(t, observed)
				require.NoError(t, mock.ExpectationsWereMet())
			})
		}
	}
}
