package service

import (
	"io"
	"time"
)

// Binary protocol adapters observe decoded provider frames. Raw EventStream
// bytes cannot establish semantic output or a successful terminal event.
type schedulingFrameState struct {
	Semantic, Terminal, Failed, TimedOut, Excluded bool
	FirstSemanticMS                                int
}

type schedulingFrameObserver interface {
	BeginSchedulingFrames()
	ObserveSchedulingFrame([]byte) schedulingFrameState
	SchedulingFrameState() schedulingFrameState
	FinishSchedulingFrames(error)
}

func (b *controlledResponseBody) BeginSchedulingFrames() {
	b.decoded = true
	b.dispatch.mu.Lock()
	b.dispatch.semanticObservable = true
	b.dispatch.startFirstOutputTimerLocked()
	b.dispatch.mu.Unlock()
}

func (b *controlledResponseBody) ObserveSchedulingFrame(frame []byte) schedulingFrameState {
	b.dispatch.ObserveFrame(frame)
	return b.SchedulingFrameState()
}

func (b *controlledResponseBody) SchedulingFrameState() schedulingFrameState {
	d := b.dispatch
	d.mu.Lock()
	defer d.mu.Unlock()
	state := schedulingFrameState{Semantic: !d.semantic.IsZero(), Terminal: d.terminal, Failed: d.upstreamFailure, TimedOut: d.timeout, Excluded: d.excluded}
	if state.Semantic {
		state.FirstSemanticMS = int(d.semantic.Sub(d.started) / time.Millisecond)
	}
	return state
}

func (b *controlledResponseBody) FinishSchedulingFrames(err error) {
	state := b.SchedulingFrameState()
	outcome := "stream_error"
	terminal := state.Terminal
	if err == io.EOF {
		err = nil
		outcome = "completed"
		if !terminal || !state.Semantic {
			err = io.ErrUnexpectedEOF
			terminal = false
			outcome = "stream_truncated"
		}
	}
	b.dispatch.Finish(outcome, terminal, err)
}
