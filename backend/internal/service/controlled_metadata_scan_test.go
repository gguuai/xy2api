package service

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"io"
	"strings"
	"testing"
	"time"
)

func TestControlledLargeMetadataReusesHandlerSnapshot(t *testing.T) {
	payload := strings.Repeat("large content ", 40000)
	body := []byte("{\"input\":\"" + payload + "\",\"model\":\"large-model\",\"stream\":true,\"reasoning\":{\"effort\":\"high\"},\"tools\":[{\"type\":\"function\",\"parameters\":{\"description\":\"" + payload + "\"}}]}")
	ctx := NewControlledRequestContext(context.Background(), "responses")
	r := controlledRequest(ctx)
	reader := &schedulingMetadataBody{ReadCloser: io.NopCloser(strings.NewReader(string(body))), request: r}
	actual, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, body, actual)
	require.True(t, reader.truncated)
	require.False(t, r.ReplaySafe)
	CaptureControlledRequestMetadata(ctx, actual, "large-model", true)
	require.Equal(t, "high", r.Reasoning)
	require.Equal(t, "large-model", r.Model)
	require.True(t, r.Stream)
	require.True(t, r.ReplaySafe)
	require.EqualValues(t, -1, r.ContextTokens)
	r.policyLoaded = true
	CaptureControlledRequestMetadata(ctx, []byte("{\"reasoning\":{\"effort\":\"low\"}}"), "different", false)
	require.Equal(t, "high", r.Reasoning)
	require.Equal(t, "large-model", r.Model)
	model, stream, valid := ReadControlledOutboundMetadata(strings.NewReader(string(body)))
	require.True(t, valid)
	require.Equal(t, "large-model", model)
	require.True(t, stream)

}
func TestControlledMetadataMalformedAndUnsafeTools(t *testing.T) {
	for _, body := range []string{"{\"tools\":[{\"type\":\"web_search\"}]}", "{\"tools\":{}}", "{\"tools\":true}", "{\"tools\":[null]}", "{\"tools\":[\"function\"]}", "{\"tools\":[{\"type\":\"function\"}],}"} {
		ctx := NewControlledRequestContext(context.Background(), "responses")
		CaptureControlledRequestMetadata(ctx, []byte(body), "m", true)
		require.False(t, controlledRequest(ctx).ReplaySafe, body)
	}
	for _, body := range []string{"{\"stream\":\"true\"}", "{\"stream\":true} garbage", "[]", "null", "{\"input\":\"unterminated}"} {
		_, _, valid := ReadControlledOutboundMetadata(strings.NewReader(body))
		require.False(t, valid, body)
	}
	ctx := NewControlledRequestContext(context.Background(), "gemini")
	CaptureControlledRequestMetadata(ctx, []byte("{\"generationConfig\":{\"thinkingConfig\":{\"thinkingLevel\":\"HIGH\"}},\"tools\":[{\"functionDeclarations\":[]}]}"), "from-url", true)
	r := controlledRequest(ctx)
	require.Equal(t, "from-url", r.Model)
	require.True(t, r.Stream)
	require.Equal(t, "high", r.Reasoning)
	require.False(t, r.ReplaySafe)
}
func TestControlledWSMetadataAndSelectedFields(t *testing.T) {
	ctx := NewControlledRequestContext(context.Background(), "ws")
	started := time.Now().Add(-time.Second)
	CaptureControlledWSFirstRequest(ctx, []byte("{\"model\":\"m\",\"stream\":true,\"reasoning\":{\"effort\":\"low\"}}"), started)
	r := controlledRequest(ctx)
	require.Equal(t, started, r.Started)
	require.Equal(t, "low", r.Reasoning)
	fields, _, valid := readSchedulingMetadata(strings.NewReader("{\"input\":\"secret\",\"reasoning\":{\"effort\":\"high\",\"unexpected\":\"secret\"}}"), "responses", "root")
	require.True(t, valid)
	raw, _ := json.Marshal(fields)
	require.NotContains(t, string(raw), "secret")
}

// Run independently with -run '^$' -bench BenchmarkControlledMetadataBoundedRetention
// -benchmem. Payload allocation is outside the timer, so B/op measures parser
// retention across 1 KiB, 1 MiB and 10 MiB prompts without suite goroutine noise.
func BenchmarkControlledMetadataBoundedRetention(b *testing.B) {
	for _, size := range []struct {
		name  string
		bytes int
	}{{"1KiB", 1 << 10}, {"1MiB", 1 << 20}, {"10MiB", 10 << 20}} {
		b.Run(size.name, func(b *testing.B) {
			payload := `{"input":"` + strings.Repeat("x", size.bytes) + `","model":"m","stream":true}`
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				model, stream, valid := ReadControlledOutboundMetadata(strings.NewReader(payload))
				if !valid || model != "m" || !stream {
					b.Fatal("metadata mismatch")
				}
			}
		})
	}
}
