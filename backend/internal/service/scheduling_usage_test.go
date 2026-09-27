package service

import (
	"context"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/liulixin-lex/xy2api/internal/config"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSchedulingUsageSnapshotStaysWithSubmittedAttempt(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx := NewControlledRequestContext(parent, "ws")
	r := controlledRequest(ctx)
	r.history = []SchedulingAttemptTrace{{AttemptID: "ticket-one", AccountID: 1}}
	frozen := CaptureSchedulingUsageContext(ctx)
	r.history = append(r.history, SchedulingAttemptTrace{AttemptID: "ticket-two", AccountID: 2})
	cancel()
	worker := CopySchedulingUsageContext(context.Background(), frozen)
	require.Equal(t, "ticket-one", SchedulingAttemptIDFromContext(worker))
	require.Equal(t, "ticket-two", SchedulingAttemptIDFromContext(ctx))
	require.NoError(t, worker.Err())
	explicit := WithSchedulingUsageAttempt(ctx, "ticket-three")
	require.Equal(t, "ticket-three", SchedulingAttemptIDFromContext(CaptureSchedulingUsageContext(explicit)))
}
func TestSchedulingUsageAckRequiresDurableAccounting(t *testing.T) {
	for _, platform := range []string{"openai", "anthropic"} {
		for _, scenario := range []string{"success", "duplicate", "billing_error", "log_error", "account_mismatch"} {
			t.Run(platform+"_"+scenario, func(t *testing.T) {
				db, m, err := sqlmock.New()
				require.NoError(t, err)
				defer db.Close()
				store := scheduling.NewPostgresStore(db)
				control := &ControlledSchedulingService{Store: store}
				logs := &openAIRecordUsageLogRepoStub{inserted: true}
				bills := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
				if scenario == "duplicate" {
					logs.inserted = false
					bills.result.Applied = false
				}
				if scenario == "billing_error" {
					bills.err = errors.New("billing unavailable")
				}
				if scenario == "log_error" {
					logs.err = errors.New("durable log unavailable")
				}
				if scenario == "success" || scenario == "duplicate" || scenario == "account_mismatch" {
					accountID := int64(3000)
					if scenario == "account_mismatch" {
						accountID = 3001
					}
					m.ExpectQuery("SELECT request_id,account_id,family_id,account_epoch,family_epoch,lease_until,state FROM scheduling_attempts").WithArgs("ticket-one").WillReturnRows(sqlmock.NewRows([]string{"request_id", "account_id", "family_id", "account_epoch", "family_epoch", "lease_until", "state"}).AddRow("request", accountID, accountID, 0, 0, time.Now(), "settled"))
					if scenario != "account_mismatch" {
						m.ExpectExec("UPDATE scheduling_attempts SET usage_pending=FALSE,usage_acknowledged=TRUE WHERE ticket_id").WithArgs("ticket-one").WillReturnResult(sqlmock.NewResult(0, 1))
					}
				}
				ctx := WithSchedulingUsageAttempt(context.Background(), "ticket-one")
				key := &APIKey{ID: 1000, Quota: 100, Group: &Group{RateMultiplier: 1}}
				user := &User{ID: 2000}
				account := &Account{ID: 3000, Type: AccountTypeAPIKey}
				quota := &openAIRecordUsageAPIKeyQuotaStub{}
				if platform == "openai" {
					svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(logs, bills, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
					svc.controlledScheduling = control
					err = svc.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: &OpenAIForwardResult{RequestID: "response-one", Model: "gpt-5.1", Duration: time.Second}, APIKey: key, User: user, Account: account, APIKeyService: quota})
				} else {
					svc := newSchedulingGatewayRecordUsageService(logs, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
					svc.usageBillingRepo = bills
					svc.controlledScheduling = control
					err = svc.RecordUsage(ctx, &RecordUsageInput{Result: &ForwardResult{RequestID: "response-one", Model: "claude-sonnet-4", Duration: time.Second}, APIKey: key, User: user, Account: account, APIKeyService: quota})
				}
				if scenario == "success" || scenario == "duplicate" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
				require.NoError(t, m.ExpectationsWereMet())
				require.Equal(t, 1, bills.calls)
			})
		}
	}
}

func newSchedulingGatewayRecordUsageService(usageRepo UsageLogRepository, userRepo UserRepository, subRepo UserSubscriptionRepository) *GatewayService {
	cfg := &config.Config{}
	cfg.Default.RateMultiplier = 1.1
	return NewGatewayService(
		nil,
		nil,
		usageRepo,
		nil,
		userRepo,
		subRepo,
		nil,
		nil,
		cfg,
		nil,
		nil,
		NewBillingService(cfg, nil),
		nil,
		&BillingCacheService{},
		nil,
		nil,
		&DeferredService{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil, // userPlatformQuotaRepo
	)
}
