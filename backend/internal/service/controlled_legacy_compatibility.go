package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/liulixin-lex/xy2api/internal/scheduling"
)

// IsControlledSchedulingStop identifies local admission/ledger decisions. They
// are not upstream faults and must retain their identity through adapter layers.
func IsControlledSchedulingStop(err error) bool {
	return errors.Is(err, scheduling.ErrCommitted) ||
		errors.Is(err, scheduling.ErrAttemptBudget) ||
		errors.Is(err, scheduling.ErrDeadline) ||
		errors.Is(err, scheduling.ErrRetryBudget) ||
		errors.Is(err, scheduling.ErrUnsafeReplay) ||
		errors.Is(err, scheduling.ErrSharedState) ||
		errors.Is(err, scheduling.ErrControlBlocked) ||
		errors.Is(err, scheduling.ErrAttemptIdentity) ||
		errors.Is(err, scheduling.ErrCapacity)
}

// Only inference transport errors reach this helper. Local admission decisions
// and client cancellation retain their identity for the outer request loop.
func controlledSchedulingTransportFailure(ctx context.Context, err error) error {
	if !ControlledSchedulingEnabled(ctx) || IsControlledSchedulingStop(err) || ctx.Err() != nil {
		return err
	}
	var failover *UpstreamFailoverError
	if errors.As(err, &failover) {
		return failover
	}
	status := http.StatusBadGateway
	body := gatewayTransportFailoverBody
	if strings.Contains(err.Error(), "first_output_timeout") {
		status = http.StatusGatewayTimeout
		body = []byte("{\"error\":{\"type\":\"first_output_timeout\",\"message\":\"Upstream produced no semantic output before the deadline\"}}")
	}
	return &UpstreamFailoverError{StatusCode: status, ResponseBody: body, SafeToFailoverAfterWrite: true}
}
