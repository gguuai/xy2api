package service

import (
	"context"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestControlledWSFinalErrorDefersClientCommitToOuterRetry(t *testing.T) {
	for _, input := range []error{errors.New("socket reset"), scheduling.ErrControlBlocked, scheduling.ErrRetryBudget, &UpstreamFailoverError{StatusCode: 503}} {
		ctx := NewControlledRequestContext(context.Background(), "ws")
		r := controlledRequest(ctx)
		r.policyLoaded = true
		r.Policy.Enabled = true
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		s := &OpenAIGatewayService{}
		err := s.finalizeControlledWSError(ctx, c, &Account{ID: 1}, input)
		require.False(t, c.Writer.Written())
		require.Empty(t, w.Body.String())
		if IsControlledSchedulingStop(input) {
			require.ErrorIs(t, err, input)
		} else {
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
		}
	}
}
