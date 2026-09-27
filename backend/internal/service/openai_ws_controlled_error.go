package service

import (
	"context"
	"github.com/gin-gonic/gin"
)

// The outer controlled request owns retry and terminal client errors. Writing a
// fallback here would commit the response before its next-account decision.
func (s *OpenAIGatewayService) finalizeControlledWSError(ctx context.Context, c *gin.Context, a *Account, err error) error {
	if ControlledSchedulingEnabled(ctx) {
		return controlledSchedulingTransportFailure(ctx, err)
	}
	s.writeOpenAIWSFallbackErrorResponse(c, a, err)
	return err
}
