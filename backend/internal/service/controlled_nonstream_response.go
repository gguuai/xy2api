package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/tidwall/gjson"
)

type controlledBufferedResponseContextKey struct{}

// nonstreamReadError classifies only transport read failures before an adapter
// has accepted a response. Account selection still enforces its own admission
// and retry budget; this helper never spends or resets either.
func nonstreamReadError(ctx context.Context, resp *http.Response, err error) error {
	if err == nil || ctx == nil || ctx.Err() != nil || resp == nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, ErrUpstreamResponseBodyTooLarge) {
		return err
	}
	var transportErr net.Error
	retryable := errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, context.DeadlineExceeded) || errors.As(err, &transportErr)
	if !retryable {
		return err
	}
	if r := controlledRequest(ctx); r != nil {
		r.mu.Lock()
		blocked := !r.ReplaySafe || r.owner || !r.semanticAt.IsZero()
		ledger, limit := r.Ledger, r.Policy.Retry.MaxAttempts
		r.mu.Unlock()
		if blocked {
			return err
		}
		if ledger != nil {
			snapshot := ledger.Snapshot()
			if snapshot.Committed || (!snapshot.Deadline.IsZero() && !time.Now().Before(snapshot.Deadline)) ||
				(limit > 0 && snapshot.Attempts >= limit) {
				return err
			}
		}
	}
	return &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ClientMessage: "failed to read upstream response", cause: err}
}

// Buffered adapters own acceptance even when their upstream wire format is SSE.
// Mark this before dispatch: transport prefetch must not settle or cap an image
// result before the adapter can parse it. This does not change client streaming.
func withControlledBufferedResponse(ctx context.Context) context.Context {
	return context.WithValue(ctx, controlledBufferedResponseContextKey{}, true)
}

// Some buffered SSE adapters stop at a parsed terminal instead of draining EOF.
// Record remote completion only; acceptance and health remain with the adapter.
func markControlledNonstreamTerminal(resp *http.Response, payload ...[]byte) {
	if resp == nil {
		return
	}
	b, ok := resp.Body.(*controlledResponseBody)
	if !ok || !b.success || !b.buffered || b.decoded {
		return
	}
	for _, frame := range payload {
		observeControlledBufferedFailure(resp, frame)
	}
	b.dispatch.mu.Lock()
	b.dispatch.transportTerminal = true
	if b.dispatch.timer != nil {
		b.dispatch.timer.Stop()
	}
	b.dispatch.mu.Unlock()
}

// Buffered adapters bypass the semantic parser, but explicit failure frames still
// carry provider attribution and request replay constraints. Observe only these
// negative facts; buffered completion never supplies a first-output sample.
func observeControlledBufferedFailure(resp *http.Response, payload []byte) {
	if resp == nil || !gjson.ValidBytes(payload) {
		return
	}
	b, ok := resp.Body.(*controlledResponseBody)
	if !ok || !b.success || !b.buffered || b.decoded {
		return
	}
	b.dispatch.request.mu.Lock()
	enabled := b.dispatch.request.Policy.Enabled
	b.dispatch.request.mu.Unlock()
	if !enabled {
		return
	}
	v := gjson.ParseBytes(payload)
	kind, status := v.Get("type").String(), controlledProtocolResponseStatus(v)
	failure := kind == "error" || kind == "response.failed" || status == "failed"
	incomplete := kind == "response.incomplete" || kind == "response.cancelled" || kind == "response.canceled" ||
		status == "incomplete" || status == "cancelled" || status == "canceled"
	requestFailure := false
	for _, path := range []string{"error", "response.error"} {
		detail := v.Get(path)
		if !detail.IsObject() {
			continue
		}
		failure = true
		for _, field := range []string{"code", "type"} {
			switch detail.Get(field).String() {
			case "invalid_request_error", "invalid_request", "context_length_exceeded", "content_policy_violation", "cyber_policy":
				requestFailure = true
			}
		}
	}
	if !failure && !incomplete {
		return
	}
	b.dispatch.mu.Lock()
	if requestFailure || incomplete {
		b.dispatch.excluded = true
	} else {
		b.dispatch.upstreamFailure = true
		b.dispatch.observeProtocolFailureLocked(v)
	}
	b.dispatch.mu.Unlock()
	if requestFailure {
		b.dispatch.request.mu.Lock()
		b.dispatch.request.ReplaySafe = false
		b.dispatch.request.mu.Unlock()
	}
}

// Nonstream EOF confirms that remote work ended. Only the protocol adapter can
// confirm that its parsed response was accepted. Both facts feed the existing
// one-shot settlement; no transport EOF can grant positive health prematurely.
func validateControlledNonstreamResponse(resp *http.Response, body []byte, protocol string) error {
	if resp == nil {
		return nil
	}
	b, ok := resp.Body.(*controlledResponseBody)
	if !ok || !b.success || (b.sse && !b.buffered) || b.decoded {
		return nil
	}
	var err error
	if protocol == "grok_voice" {
		contentType := strings.ToLower(resp.Header.Get("Content-Type"))
		if strings.HasPrefix(contentType, "audio/") || strings.HasPrefix(contentType, "application/octet-stream") {
			protocol = "voice_audio"
		} else {
			protocol = "voice_json"
		}
	}
	valid, healthy := controlledNonstreamSuccess(body, protocol)
	b.dispatch.request.mu.Lock()
	enabled := b.dispatch.request.Policy.Enabled
	b.dispatch.request.mu.Unlock()
	if !enabled {
		valid, healthy = true, true
	} else if b.buffered {
		if bodyHasSSEFraming(body) {
			forEachOpenAISSEFrame(string(body), func(_ string, frame []byte) {
				observeControlledBufferedFailure(resp, frame)
			})
		} else {
			observeControlledBufferedFailure(resp, body)
		}
	}
	if !valid {
		err = &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ClientMessage: "upstream returned an invalid completion"}
	}
	b.dispatch.mu.Lock()
	b.nonstreamValidated, b.nonstreamValidationErr = err == nil && healthy, err
	b.dispatch.mu.Unlock()
	return err
}

// An adapter can reject an upstream payload before envelope validation. Preserve
// its native error while distinguishing provider parse/server failures from
// local conversion errors and request-specific content-policy rejections.
func rejectControlledNonstreamResponse(resp *http.Response, err error) {
	if resp == nil || err == nil {
		return
	}
	b, ok := resp.Body.(*controlledResponseBody)
	if !ok || !b.success || (b.sse && !b.buffered) || b.decoded {
		return
	}
	b.dispatch.request.mu.Lock()
	enabled := b.dispatch.request.Policy.Enabled
	b.dispatch.request.mu.Unlock()
	if !enabled {
		return
	}
	b.dispatch.mu.Lock()
	b.nonstreamValidated, b.nonstreamValidationErr = false, err
	b.dispatch.mu.Unlock()
}

func finishControlledNonstreamResponse(resp *http.Response, adapterErr *error) {
	if resp == nil {
		return
	}
	b, ok := resp.Body.(*controlledResponseBody)
	if !ok || !b.success || (b.sse && !b.buffered) || b.decoded {
		return
	}
	b.dispatch.mu.Lock()
	terminal, valid, validationErr := b.dispatch.transportTerminal, b.nonstreamValidated, b.nonstreamValidationErr
	upstreamFailure := b.dispatch.upstreamFailure
	b.dispatch.mu.Unlock()
	err := *adapterErr
	if err == nil {
		err = validationErr
	}
	if err != nil {
		// Images adapters can return a parsed request rejection before generic
		// envelope validation. Keep the native error while prohibiting replay;
		// a 400 verdict is not evidence of provider unavailability.
		var imageErr *OpenAIImagesUpstreamError
		if errors.As(err, &imageErr) && imageErr.StatusCode == http.StatusBadRequest {
			b.dispatch.request.mu.Lock()
			enabled := b.dispatch.request.Policy.Enabled
			if enabled {
				b.dispatch.request.ReplaySafe = false
			}
			b.dispatch.request.mu.Unlock()
			if enabled {
				b.dispatch.mu.Lock()
				b.dispatch.excluded = true
				b.dispatch.mu.Unlock()
			}
		}
		var upstream *UpstreamFailoverError
		if validationErr == nil && !upstreamFailure && !errors.As(err, &upstream) {
			// Adapter conversion and downstream writing can fail after a valid
			// upstream body. They are not evidence of provider unavailability.
			b.dispatch.mu.Lock()
			b.dispatch.excluded = true
			b.dispatch.mu.Unlock()
		}
		b.dispatch.Finish("invalid_response", terminal, err)
	} else if valid && terminal {
		b.dispatch.Finish("completed", true, nil)
	} else {
		b.dispatch.Finish("response_unvalidated", terminal, nil)
	}
}

// These checks are deliberately limited to the terminal envelope. Provider
// payload conversion/usage validation still belongs to its existing adapter.
// Error objects, null, empty bodies and unrelated JSON cannot prove availability.
func controlledNonstreamSuccess(body []byte, protocol string) (bool, bool) {
	if protocol == "voice_audio" {
		return len(body) > 0, len(body) > 0
	}
	if bodyHasSSEFraming(body) {
		if protocol != "responses" {
			return false, false
		}
		semantic, terminal, failed, completedEnvelope := false, false, false, false
		toolPending := false
		observe := func(frame []byte) {
			if !gjson.ValidBytes(frame) {
				return
			}
			s, _, t, tool := classifySemanticEvent(frame)
			toolPending = toolPending || tool
			if t && toolPending {
				s, toolPending = true, false
			}
			semantic, terminal = semantic || s, terminal || t
			kind := gjson.GetBytes(frame, "type").String()
			if kind == "error" || kind == "response.failed" || kind == "response.incomplete" || kind == "response.cancelled" {
				failed, terminal = true, true
			}
			if kind == "response.completed" || kind == "response.done" {
				response := gjson.GetBytes(frame, "response")
				status := response.Get("status").String()
				if status == "failed" || status == "incomplete" || status == "cancelled" ||
					(response.Get("error").Exists() && response.Get("error").Type != gjson.Null) {
					failed, terminal = true, true
				} else if response.IsObject() && response.Get("output").IsArray() && (status == "" || status == "completed") {
					completedEnvelope, terminal = true, true
				}
			}
		}
		// This body is already bounded by the adapter's response-size limit.
		// Use its protocol parser, not the latency parser's smaller frame cap:
		// completed images can be large and contain no first-semantic delta.
		var frames openAISSEDataAccumulator
		for _, line := range strings.Split(string(body), "\n") {
			if data, ok := extractOpenAISSEDataLine(strings.TrimRight(line, "\r")); ok && strings.TrimSpace(data) == "[DONE]" {
				terminal = true
			}
			frames.AddLine(line, observe)
		}
		frames.Flush(observe)
		// A terminal event name alone is not an accepted response. Keep valid
		// empty completed envelopes and explicit failure terminals distinct
		// from malformed response.completed or bare [DONE] with no output.
		valid := terminal && (semantic || completedEnvelope || failed)
		return valid, valid && (semantic || completedEnvelope) && !failed
	}
	if !gjson.ValidBytes(body) {
		return false, false
	}
	v := gjson.ParseBytes(body)
	if !v.IsObject() || (v.Get("error").Exists() && v.Get("error").Type != gjson.Null) {
		return false, false
	}
	switch protocol {
	case "messages":
		valid := v.Get("type").String() == "message" && v.Get("content").IsArray() && v.Get("stop_reason").String() != ""
		return valid, valid
	case "responses":
		if !v.Get("output").IsArray() {
			return false, false
		}
		switch v.Get("status").String() {
		case "completed":
			return true, true
		case "incomplete", "cancelled", "failed":
			return true, false
		}
		compact := v.Get("object").String() == "response.compaction"
		return compact, compact
	case "chat":
		choices := v.Get("choices").Array()
		if len(choices) == 0 {
			return false, false
		}
		for _, choice := range choices {
			if !choice.Get("message").IsObject() || choice.Get("finish_reason").String() == "" {
				return false, false
			}
		}
		return true, true
	case "gemini":
		if nested := v.Get("response"); nested.IsObject() {
			v = nested
		}
		if v.Get("promptFeedback.blockReason").String() != "" {
			return true, false
		}
		candidates := v.Get("candidates").Array()
		if len(candidates) == 0 || v.Get("error").Exists() {
			return false, false
		}
		healthy := true
		for _, candidate := range candidates {
			reason := candidate.Get("finishReason").String()
			if reason == "" {
				return false, false
			}
			if !candidate.Get("content.parts").IsArray() {
				if reason == "STOP" || reason == "MAX_TOKENS" {
					return false, false
				}
				healthy = false
			}
		}
		return true, healthy
	case "count_tokens":
		_, valid := boundedJSONNonNegativeInt(v.Get("input_tokens"))
		return valid, valid
	case "embeddings", "images", "grok_images":
		// Passthrough adapters do not parse these outputs further. An object
		// placeholder alone must not release a failed provider's recovery gate.
		items := v.Get("data").Array()
		if len(items) == 0 {
			return false, false
		}
		for _, item := range items {
			if !item.IsObject() {
				return false, false
			}
			if protocol == "embeddings" {
				embedding := item.Get("embedding")
				// OpenAI-compatible providers support float vectors and base64
				// vectors. Preserve the encoded bytes without decoding/copying.
				if embedding.Type == gjson.String && strings.TrimSpace(embedding.String()) != "" {
					continue
				}
				if !embedding.IsArray() || len(embedding.Array()) == 0 {
					return false, false
				}
				for _, value := range embedding.Array() {
					if value.Type != gjson.Number {
						return false, false
					}
				}
				continue
			}
			url, encoded := item.Get("url"), item.Get("b64_json")
			if (url.Type != gjson.String || strings.TrimSpace(url.String()) == "") &&
				(encoded.Type != gjson.String || strings.TrimSpace(encoded.String()) == "") {
				return false, false
			}
		}
		return true, true
	case "alpha_search":
		output, encrypted := v.Get("output"), v.Get("encrypted_output")
		valid := output.Type == gjson.String || output.IsArray() || v.Get("results").IsArray() ||
			(encrypted.Type == gjson.String && strings.TrimSpace(encrypted.String()) != "")
		return valid, valid
	case "voice_json":
		// Empty transcription is a valid silence result. TTS with timestamps
		// carries encoded audio; custom voice creation returns its voice ID.
		text, audio, voice := v.Get("text"), v.Get("audio"), v.Get("voice_id")
		valid := text.Type == gjson.String || v.Get("voices").IsArray() ||
			(audio.Type == gjson.String && strings.TrimSpace(audio.String()) != "") ||
			(voice.Type == gjson.String && strings.TrimSpace(voice.String()) != "")
		return valid, valid
	case "grok_video_create":
		valid := extractGrokMediaVideoRequestID(body) != ""
		return valid, valid
	case "grok_media":
		valid := v.Get("id").String() != "" || v.Get("request_id").String() != "" || v.Get("status").String() != ""
		if !valid {
			items := v.Get("data").Array()
			valid = len(items) > 0 && items[0].IsObject()
		}
		return valid, valid
	case "seedance", "seedance_create":
		valid := v.Get("id").String() != ""
		return valid, valid
	}
	return false, false
}

// HTTP error bodies have no TTFT. Keep their diagnostic reads bounded by D,
// including providers which fill the evidence prefix and then stall forever.
// With no configured D a short diagnostic cap still bounds error-body draining.
func (d *controlledDispatch) startErrorBodyTimerLocked() {
	if d.timer != nil {
		d.timer.Stop()
	}
	d.request.mu.Lock()
	enabled, ledger := d.request.Policy.Enabled, d.request.Ledger
	d.request.mu.Unlock()
	if !enabled || ledger == nil {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	if total := ledger.Snapshot().Deadline; !total.IsZero() && total.Before(deadline) {
		deadline = total
	}
	d.timer = time.AfterFunc(time.Until(deadline), func() {
		d.mu.Lock()
		d.responseDrainTimeout = true
		d.mu.Unlock()
		d.cancel()
	})
}

func (d *controlledDispatch) responseReadError(err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return err
	}
	d.mu.Lock()
	timedOut := d.responseDrainTimeout
	attemptTimedOut := d.timeout
	d.mu.Unlock()
	if timedOut {
		return errors.New("upstream error body deadline exceeded")
	}
	// The attempt timer cancels only the upstream child context. Keep that
	// deadline distinct from caller/admin cancellation so buffered adapters can
	// hand a pre-output timeout to the existing, budgeted failover handler.
	if attemptTimedOut && !d.adminCancelled.Load() && d.request != nil &&
		(d.request.clientContext == nil || d.request.clientContext.Err() == nil) {
		return fmt.Errorf("upstream attempt deadline exceeded: %w", context.DeadlineExceeded)
	}
	return err
}
