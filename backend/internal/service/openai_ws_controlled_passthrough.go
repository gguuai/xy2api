package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	coderws "github.com/coder/websocket"
	openaiwsv2 "github.com/liulixin-lex/xy2api/internal/service/openai_ws_v2"
	"github.com/tidwall/gjson"
)

// The relay may wait for an upstream frame before the client submits its next
// turn. A session cancellation bridge must therefore follow the active attempt,
// not snapshot the preceding turn's context when ReadFrame starts.
type controlledPassthroughAttempt interface {
	Context() context.Context
	MarkSent() error
	ObserveFrame([]byte)
	CommitOutput([]byte)
	Finish(string, bool, error)
}

type openAIWSControlledPassthroughFrameConn struct {
	inner         openaiwsv2.FrameConn
	ctx           context.Context
	cancel        context.CancelCauseFunc
	prepare       func([]byte, bool) (context.Context, controlledPassthroughAttempt, error)
	mu            sync.Mutex
	active        controlledPassthroughAttempt
	activeNewTurn bool
	requestCtx    context.Context
	finishedCtx   context.Context
	settleUsage   func(context.Context)
	stopWatch     func() bool
	turns         int
}

func newOpenAIWSControlledPassthroughFrameConn(ctx context.Context, inner openaiwsv2.FrameConn, prepare func([]byte, bool) (context.Context, controlledPassthroughAttempt, error)) *openAIWSControlledPassthroughFrameConn {
	live, cancel := context.WithCancelCause(ctx)
	return &openAIWSControlledPassthroughFrameConn{inner: inner, ctx: live, cancel: cancel, prepare: prepare}
}

func (c *openAIWSControlledPassthroughFrameConn) turnContext() context.Context {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requestCtx
}

func (c *openAIWSControlledPassthroughFrameConn) activeAttempt() controlledPassthroughAttempt {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

func (c *openAIWSControlledPassthroughFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	readCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer cancel()
	typ, payload, err := c.inner.ReadFrame(readCtx)
	if err != nil && c.ctx.Err() != nil {
		return typ, payload, context.Cause(c.ctx)
	}
	if err == nil && (typ == coderws.MessageText || typ == coderws.MessageBinary) {
		if d := c.activeAttempt(); d != nil {
			d.ObserveFrame(payload)
		}
	}
	return typ, payload, err
}

func (c *openAIWSControlledPassthroughFrameConn) WriteFrame(ctx context.Context, typ coderws.MessageType, payload []byte) error {
	if c.ctx.Err() != nil {
		return context.Cause(c.ctx)
	}
	isCreate := (typ == coderws.MessageText || typ == coderws.MessageBinary) && strings.TrimSpace(gjson.GetBytes(payload, "type").String()) == "response.create"
	if !isCreate {
		return c.inner.WriteFrame(ctx, typ, payload)
	}
	c.mu.Lock()
	active := c.active
	newTurn := c.turns > 0
	c.mu.Unlock()
	if active != nil {
		return errors.New("overlapping controlled response.create is not supported")
	}
	turnCtx, d, err := c.prepare(payload, newTurn)
	if err != nil {
		if newTurn && turnCtx != nil {
			if r := controlledRequest(turnCtx); r != nil {
				r.Close()
			}
		}
		return err
	}
	c.mu.Lock()
	c.requestCtx = turnCtx
	c.active = d
	c.activeNewTurn = newTurn
	c.mu.Unlock()
	if d != nil {
		if err = d.MarkSent(); err != nil {
			c.finishTurn("not_sent", true, err)
			return err
		}
		c.mu.Lock()
		c.stopWatch = context.AfterFunc(d.Context(), func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.active == d {
				c.cancel(controlledPassthroughCancelCause(d))
			}
		})
		c.mu.Unlock()
	}
	c.mu.Lock()
	c.turns++
	c.mu.Unlock()
	writeCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer cancel()
	err = c.inner.WriteFrame(writeCtx, typ, payload)
	if err != nil {
		c.finishTurn("write_failed", false, err)
	}
	return err
}

func (c *openAIWSControlledPassthroughFrameConn) commitOutput(payload []byte) {
	if c == nil {
		return
	}
	if d := c.activeAttempt(); d != nil {
		d.CommitOutput(payload)
	}
}

// Relay discovers terminal usage before writing the terminal frame downstream.
// Bill that turn only after its dispatch has settled so acknowledgement cannot
// race a later usage_pending write, and never borrow the first turn's ticket.
func (c *openAIWSControlledPassthroughFrameConn) afterTurnSettlement(fn func(context.Context)) {
	c.mu.Lock()
	if c.requestCtx != nil {
		c.settleUsage = fn
		c.mu.Unlock()
		return
	}
	ctx := c.finishedCtx
	c.mu.Unlock()
	fn(ctx)
}

func (c *openAIWSControlledPassthroughFrameConn) finishTurn(outcome string, terminal bool, err error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	d := c.active
	requestCtx := c.requestCtx
	settleUsage := c.settleUsage
	c.settleUsage = nil
	if requestCtx != nil {
		c.finishedCtx = requestCtx
	}
	newTurn := c.activeNewTurn
	c.active = nil
	c.activeNewTurn = false
	c.requestCtx = nil
	if c.stopWatch != nil {
		c.stopWatch()
		c.stopWatch = nil
	}
	c.mu.Unlock()
	if d != nil {
		d.Finish(outcome, terminal, err)
	}
	if settleUsage != nil {
		settleUsage(requestCtx)
	}
	// The initial turn may be retried by its handler and retains its ledger.
	if newTurn && requestCtx != nil {
		if r := controlledRequest(requestCtx); r != nil {
			r.Close()
		}
	}
}

func (c *openAIWSControlledPassthroughFrameConn) Close() error {
	c.finishTurn("connection_closed", false, context.Cause(c.ctx))
	c.cancel(context.Canceled)
	return c.inner.Close()
}

func controlledPassthroughCancelCause(attempt controlledPassthroughAttempt) error {
	if d, ok := attempt.(*controlledDispatch); ok {
		d.mu.Lock()
		timeout, clipped, started := d.timeout, d.clipped, d.started
		d.mu.Unlock()
		if timeout && !clipped {
			d.request.mu.Lock()
			profile, model, reasoning := d.request.Profile, d.request.Model, d.request.Reasoning
			d.request.mu.Unlock()
			return &openAIWSPassthroughFirstOutputTimeoutError{deadline: openAIWSPassthroughFirstOutputDeadline{timeout: time.Duration(profile.AttemptTimeoutMS) * time.Millisecond, startedAt: started, requestModel: model, reasoningEffort: reasoning}}
		}
		if timeout {
			return errors.New("first_output_budget_exhausted")
		}
	}
	if err := context.Cause(attempt.Context()); err != nil {
		return err
	}
	return context.Canceled
}
