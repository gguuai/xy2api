package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/liulixin-lex/xy2api/internal/scheduling"
	"github.com/stretchr/testify/require"
)

// Real AWS binary frames exercise the decoder, including CRC validation.
func controlledBedrockFrames(t *testing.T, frames ...string) []byte {
	t.Helper()
	var output bytes.Buffer
	name, value := []byte(":event-type"), []byte("chunk")
	headers := append([]byte{byte(len(name))}, name...)
	headers = append(headers, 7, 0, byte(len(value)))
	headers = append(headers, value...)
	for _, frame := range frames {
		payload, err := json.Marshal(map[string]string{"bytes": base64.StdEncoding.EncodeToString([]byte(frame))})
		require.NoError(t, err)
		b := make([]byte, 16+len(headers)+len(payload))
		binary.BigEndian.PutUint32(b[:4], uint32(len(b)))
		binary.BigEndian.PutUint32(b[4:8], uint32(len(headers)))
		binary.BigEndian.PutUint32(b[8:12], crc32.ChecksumIEEE(b[:8]))
		copy(b[12:], headers)
		copy(b[12+len(headers):], payload)
		binary.BigEndian.PutUint32(b[len(b)-4:], crc32.ChecksumIEEE(b[:len(b)-4]))
		_, _ = output.Write(b)
	}
	return output.Bytes()
}

type controlledProtocolFixture struct {
	io.Reader
	mu        sync.Mutex
	state     schedulingFrameState
	frames    []string
	began     bool
	finishErr error
}

func (b *controlledProtocolFixture) Close() error { return nil }
func (b *controlledProtocolFixture) BeginSchedulingFrames() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.began = true
}
func (b *controlledProtocolFixture) ObserveSchedulingFrame(frame []byte) schedulingFrameState {
	b.mu.Lock()
	defer b.mu.Unlock()
	semantic, _, terminal, _ := classifySemanticEvent(frame)
	b.frames = append(b.frames, string(frame))
	b.state.Semantic = b.state.Semantic || semantic
	b.state.Terminal = b.state.Terminal || terminal
	if semantic {
		b.state.FirstSemanticMS = 7
	}
	return b.state
}
func (b *controlledProtocolFixture) SchedulingFrameState() schedulingFrameState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}
func (b *controlledProtocolFixture) FinishSchedulingFrames(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.finishErr = err
}

func runControlledBedrockFixture(t *testing.T, body io.ReadCloser) (*streamingResult, *gin.Context, *httptest.ResponseRecorder, error) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	svc := &GatewayService{}
	result, err := svc.handleBedrockStreamingResponse(c.Request.Context(), &http.Response{Body: body, Header: make(http.Header)}, c, &Account{ID: 7}, time.Now(), "claude-test")
	return result, c, rec, err
}

func TestControlledBedrockDecodedFramesPreserveMetadataUntilSemantic(t *testing.T) {
	frames := []string{
		"{\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}",
		"{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"\"}}",
		"{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"reasoning\"}}",
		"{\"type\":\"message_stop\"}",
	}
	body := &controlledProtocolFixture{Reader: bytes.NewReader(controlledBedrockFrames(t, frames...))}
	result, c, rec, err := runControlledBedrockFixture(t, body)
	require.NoError(t, err)
	require.True(t, c.Writer.Written())
	require.NotNil(t, result.firstTokenMs)
	require.Equal(t, 7, *result.firstTokenMs)
	require.Contains(t, rec.Body.String(), "message_start")
	require.Contains(t, rec.Body.String(), "reasoning")
	require.Contains(t, rec.Body.String(), "message_stop")
	body.mu.Lock()
	defer body.mu.Unlock()
	require.True(t, body.began)
	require.Equal(t, frames, body.frames)
	require.ErrorIs(t, body.finishErr, io.EOF)
	require.True(t, body.state.Terminal)
}

func TestControlledBedrockEmptyAndTruncatedOutputs(t *testing.T) {
	t.Run("metadata-only remains replayable", func(t *testing.T) {
		body := &controlledProtocolFixture{Reader: bytes.NewReader(controlledBedrockFrames(t, "{\"type\":\"message_start\"}", "{\"type\":\"message_stop\"}"))}
		result, c, rec, err := runControlledBedrockFixture(t, body)
		var failover *UpstreamFailoverError
		require.ErrorAs(t, err, &failover)
		require.Equal(t, http.StatusBadGateway, failover.StatusCode)
		require.False(t, c.Writer.Written())
		require.Empty(t, rec.Body.String())
		require.Nil(t, result.firstTokenMs)
	})
	t.Run("post-semantic truncation cannot replay", func(t *testing.T) {
		body := &controlledProtocolFixture{Reader: bytes.NewReader(controlledBedrockFrames(t, "{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}"))}
		result, c, rec, err := runControlledBedrockFixture(t, body)
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
		var failover *UpstreamFailoverError
		require.False(t, errors.As(err, &failover))
		require.True(t, c.Writer.Written())
		require.Contains(t, rec.Body.String(), "hello")
		require.NotNil(t, result.firstTokenMs)
	})
	t.Run("invalid frame remains replayable", func(t *testing.T) {
		raw := controlledBedrockFrames(t, "{\"type\":\"message_start\"}")
		raw[len(raw)-1] ^= 1
		_, c, _, err := runControlledBedrockFixture(t, &controlledProtocolFixture{Reader: bytes.NewReader(raw)})
		var failover *UpstreamFailoverError
		require.ErrorAs(t, err, &failover)
		require.False(t, c.Writer.Written())
	})
	t.Run("legacy metadata handling is retained", func(t *testing.T) {
		raw := controlledBedrockFrames(t, "{\"type\":\"message_start\"}", "{\"type\":\"message_stop\"}")
		result, c, _, err := runControlledBedrockFixture(t, io.NopCloser(bytes.NewReader(raw)))
		require.NoError(t, err)
		require.NotNil(t, result.firstTokenMs)
		require.True(t, c.Writer.Written())
	})
}

func TestControlledDecodedObserverUsesSemanticTimeout(t *testing.T) {
	for _, semantic := range []bool{false, true} {
		name := "empty frame does not stop timer"
		if semantic {
			name = "thinking stops first-output timer"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := time.Now()
			profile := scheduling.LatencyProfile{AttemptTimeoutMS: 30, TotalBudgetMS: 1000, MinAttemptWindowMS: 10}
			r := &ControlledRequest{Policy: scheduling.Policy{Enabled: true}, Profile: profile, Ledger: scheduling.NewAttemptLedger(scheduling.RetryPolicy{}, profile, started, time.Time{})}
			d := &controlledDispatch{request: r, started: started, ctx: ctx, cancel: cancel, semanticReady: make(chan struct{})}
			b := &controlledResponseBody{dispatch: d}
			b.BeginSchedulingFrames()
			defer d.timer.Stop()
			state := b.ObserveSchedulingFrame([]byte("{\"type\":\"message_start\"}"))
			require.False(t, state.Semantic)
			if semantic {
				state = b.ObserveSchedulingFrame([]byte("{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"x\"}}"))
				require.True(t, state.Semantic)
				select {
				case <-ctx.Done():
					t.Fatal("semantic output must disarm first-output timeout")
				case <-time.After(70 * time.Millisecond):
				}
				require.False(t, r.Ledger.Snapshot().TimeoutSeen)
			} else {
				select {
				case <-ctx.Done():
				case <-time.After(300 * time.Millisecond):
					t.Fatal("empty frame wrongly disabled first-output timeout")
				}
				require.True(t, r.Ledger.Snapshot().TimeoutSeen)
			}
		})
	}
}

func TestControlledBinaryBodyLeavesTerminalClassificationToDecoder(t *testing.T) {
	body := &controlledResponseBody{ReadCloser: io.NopCloser(bytes.NewReader([]byte{1, 2, 3})), decoded: true, success: true}
	// A nil dispatch is deliberate: raw binary reads must neither parse nor finish.
	data, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Equal(t, []byte{1, 2, 3}, data)
}
