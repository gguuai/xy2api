//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

func TestAccountPoolInternalTimeoutIsNotClientCancellation(t *testing.T) {
	r := &ControlledRequest{Policy: scheduling.Policy{AccountPool: true}, ReplaySafe: true, clientContext: context.Background()}
	d := &controlledDispatch{request: r, timeout: true, semanticObservable: true, ticket: scheduling.DispatchTicket{Failure: &scheduling.FailureAdmission{AccountID: 11, Model: "m", HealthIdentity: "fixture"}}}
	result := d.classifyFailureDomains("first_output_timeout", context.Canceled)
	if result.Class != "first_semantic_timeout" || result.Effect != "cooldown" || result.Retry != "next_eligible" || result.Key == "" {
		t.Fatalf("an internal first-output deadline must retain attributable failure evidence: %+v", result)
	}
	client, cancel := context.WithCancel(context.Background())
	cancel()
	r.clientContext = client
	result = d.classifyFailureDomains("client_cancelled", context.Canceled)
	if result.Effect != "none" || result.Retry != "stop" {
		t.Fatalf("real client cancellation must stop retries without poisoning health: %+v", result)
	}
}

func TestAccountPoolQualityPreservesOnlyObservedExclusions(t *testing.T) {
	now := time.Now()
	state := OpenAIQualityState{Generation: 1, Binding: 11, Avoid: map[string]OpenAIQualityAvoid{"11": {Provider: "same-provider", Until: now.Add(time.Minute).UnixMilli()}}}
	a := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	if !accountPoolQualityBlocked(a, state, false, now) {
		t.Fatal("known bad account must be avoided")
	}
	a.ID = 12
	if accountPoolQualityBlocked(a, state, false, now) {
		t.Fatal("binding and evidence from another account must not override eligible weights")
	}
	a.ID = 11
	if accountPoolQualityBlocked(a, state, true, now) {
		t.Fatal("strong continuation ownership requires explicit handling, not silent migration")
	}
	a.Type = AccountTypeOAuth
	if accountPoolQualityBlocked(a, state, false, now) {
		t.Fatal("API-key quality evidence must not alter OAuth routing")
	}
}

func TestAccountPoolLocalPreparationFailureDoesNotCoolDownUpstream(t *testing.T) {
	r := &ControlledRequest{Policy: scheduling.Policy{AccountPool: true}, ReplaySafe: true, clientContext: context.Background()}
	d := &controlledDispatch{request: r, ticket: scheduling.DispatchTicket{Failure: &scheduling.FailureAdmission{AccountID: 11, Model: "m", HealthIdentity: "fixture"}}}
	result := d.classifyFailureDomains("not_sent", scheduling.ErrSharedState)
	if result.Effect != "none" || result.Key != "" {
		t.Fatalf("a local Redis/PG preparation failure must not poison the upstream: %+v", result)
	}
	d.sent = true
	result = d.classifyFailureDomains("not_sent", context.DeadlineExceeded)
	if result.Effect != "cooldown" || result.Key == "" {
		t.Fatalf("an attempted connection failure still needs attributable cooldown: %+v", result)
	}
}
