package iqcheck

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

func finalEvent(answer string) string {
	return `data: {"type":"response.completed","response":{"id":"fixture","status":"completed","model":"fixture","output":[{"type":"reasoning","content":"opaque"},{"type":"message","role":"assistant","phase":"commentary","content":"opaque"},{"type":"message","role":"assistant","phase":"final_answer","status":"completed","content":[{"type":"output_text","text":"` + answer + `"}]}]}}` + "\n\n"
}
func TestHTTPStreamContracts(t *testing.T) {
	stream := ": heartbeat\n\nevent: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"status\":\"in_progress\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"29\"}\n\n" +
		"data: {\"type\":\"response.auxiliary\",\"response\":\"extension\"}\n\n" + finalEvent("21")
	for _, tc := range []struct {
		name, body, media, status, reason string
		detected                          bool
	}{
		{"oauth stream", stream, "text/event-stream; charset=utf-8", "smart", "correct_answer", false},
		{"CRLF", strings.ReplaceAll(stream, "\n", "\r\n"), "text/event-stream", "smart", "correct_answer", false},
		{"missing content type", stream, "", "smart", "correct_answer", true},
		{"mislabeled stream", stream, "application/json", "smart", "correct_answer", true},
		{"generic media", stream, "application/octet-stream", "smart", "correct_answer", true},
		{"wrong answer", finalEvent("29"), "text/event-stream", "degraded", "wrong_answer", false},
		{"HTML", `<html>21</html>`, "text/html", "unknown", "upstream_html_response", false},
		{"bad JSON", "data: {bad}\n\n", "text/event-stream", "unknown", "invalid_event_json", false},
		{"critical duplicate", `data: {"type":"response.created","type":"response.completed"}` + "\n\n" + finalEvent("21"), "text/event-stream", "unknown", "duplicate_critical_field", false},
		{"no completion", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"21\"}\n\n", "text/event-stream", "unknown", "incomplete_response", false},
		{"conflict", finalEvent("21") + finalEvent("29"), "text/event-stream", "unknown", "conflicting_final_response", false},
		{"same completion", finalEvent("21") + finalEvent("21"), "text/event-stream", "smart", "correct_answer", false},
		{"error after completion", finalEvent("21") + "data: {\"type\":\"error\"}\n\n", "text/event-stream", "unknown", "upstream_error", false},
		{"done without completion", "data: [DONE]\n\n", "text/event-stream", "unknown", "incomplete_response", false},
		{"after done", finalEvent("21") + "data: [DONE]\n\n" + finalEvent("21"), "text/event-stream", "unknown", "data_after_done", false},
		{"wrong event name", "event: response.failed\n" + finalEvent("21"), "text/event-stream", "unknown", "event_type_mismatch", false},
		{"multiline", "id: 123\nretry: 300\ndata: {\"type\":\ndata: \"response.auxiliary\"}\n\n" + finalEvent("21"), "text/event-stream", "smart", "correct_answer", false},
		{"tail frame", strings.TrimRight(finalEvent("21"), "\n"), "text/event-stream", "smart", "correct_answer", false},
		{"bad terminal", `data: {"type":"response.completed","response":{"status":"completed","output":"21"}}` + "\n\n", "text/event-stream", "unknown", "invalid_event_shape", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {tc.media}}, Body: io.NopCloser(strings.NewReader(tc.body))}, false, "http")
			if r.Status != tc.status || r.Reason != tc.reason || r.Diagnostic == nil || r.Diagnostic.FormatDetected != tc.detected {
				t.Fatalf("%+v diagnostic=%+v", r, r.Diagnostic)
			}
			if r.Diagnostic.BytesRead > MaxResponseBytes+1 {
				t.Fatal(r.Diagnostic)
			}
		})
	}
}

type brokenReader struct{ err error }

func (r brokenReader) Read([]byte) (int, error) { return 0, r.err }
func TestHTTPBoundsCompressionAndDiagnostics(t *testing.T) {
	var zipped bytes.Buffer
	w := gzip.NewWriter(&zipped)
	_, _ = w.Write([]byte(finalEvent("21")))
	_ = w.Close()
	r := ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "Content-Encoding": {"gzip"}}, Body: io.NopCloser(&zipped)}, false, "plugin")
	if r.Status != "smart" || r.Diagnostic.Transport != "plugin" {
		t.Fatal(r)
	}
	r = ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "Content-Encoding": {"gzip"}}, Body: io.NopCloser(strings.NewReader("not-gzip"))}, false, "http")
	if r.Reason != "response_decompression_failed" {
		t.Fatal(r)
	}
	r = Parse(strings.NewReader("data: "+strings.Repeat("x", MaxEventBytes)+"\n\n"), true, false)
	if r.Reason != "event_too_large" {
		t.Fatal(r)
	}
	r = Parse(strings.NewReader(strings.Repeat("data: {\"type\":\"x\"}\n\n", MaxEvents+1)), true, false)
	if r.Reason != "too_many_events" {
		t.Fatal(r)
	}
	r = Parse(io.MultiReader(strings.NewReader(finalEvent("21")), brokenReader{context.DeadlineExceeded}), true, false)
	if r.Reason != "timeout" || r.Status != "unknown" {
		t.Fatal(r)
	}
	r = Parse(strings.NewReader(`data: {"type":"response.auxiliary","SECRET_KEY":{"type":1,"type":2}}`+"\n\n"), true, false)
	if strings.Contains(string(r.Diagnostic.JSON()), "SECRET_KEY") {
		t.Fatal(r.Diagnostic)
	}
	d := &Diagnostic{RequestID: strings.Repeat("x", 10000), EventType: strings.Repeat("x", 10000), Field: strings.Repeat("x", 10000)}
	if len(d.JSON()) > 4096 {
		t.Fatal("unbounded diagnostic")
	}
	for _, code := range []int{400, 401, 429, 500, 503} {
		r = ParseHTTP(&http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("secret"))}, false, "http")
		if r.Reason != httpCode(code) || r.Answer != "" {
			t.Fatal(r)
		}
	}
}

func TestOAuthContractFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/oauth-responses.sse")
	if err != nil {
		t.Fatal(err)
	}
	r := ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(bytes.NewReader(raw))}, false, "http")
	if r.Status != "smart" || r.Answer != `{"answer":21}` || r.Diagnostic.EventIndex != 9 {
		t.Fatalf("%+v %+v", r, r.Diagnostic)
	}
	for _, codec := range []string{"br", "zstd"} {
		var b bytes.Buffer
		if codec == "br" {
			w := brotli.NewWriter(&b)
			_, _ = w.Write(raw)
			_ = w.Close()
		} else {
			w, e := zstd.NewWriter(&b)
			if e != nil {
				t.Fatal(e)
			}
			_, _ = w.Write(raw)
			_ = w.Close()
		}
		r = ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "Content-Encoding": {codec}}, Body: io.NopCloser(&b)}, false, "plugin")
		if r.Status != "smart" {
			t.Fatal(codec, r)
		}
	}
}

func TestDiagnosticFailureEventAndCompression(t *testing.T) {
	for _, codec := range []string{"br", "zstd", "deflate"} {
		r := ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "Content-Encoding": {codec}}, Body: io.NopCloser(strings.NewReader("invalid compressed bytes"))}, false, "plugin")
		if r.Status != "unknown" || r.Reason != "response_decompression_failed" {
			t.Fatalf("%s: %+v", codec, r)
		}
	}
	r := Parse(strings.NewReader("event: response.completed\ndata: {bad}\n\n"), true, false)
	if r.Diagnostic.EventType != "response.completed" || r.Diagnostic.EventIndex != 1 {
		t.Fatal(r.Diagnostic)
	}
}

func TestHTTPCaseAliasedFields(t *testing.T) {
	output := `[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"21"}]}]`
	for _, tc := range []struct {
		name, body, media, status, reason, field string
		chat                                     bool
	}{
		{"status alias", `{"status":"incomplete","Status":"completed","output":` + output + `}`, "application/json", "unknown", "duplicate_critical_field", "root.status", false},
		{"status reverse order", `{"Status":"incomplete","status":"completed","output":` + output + `}`, "application/json", "unknown", "duplicate_critical_field", "root.status", false},
		{"Unicode status alias", `{"status":"incomplete","\u017ftatus":"completed","output":` + output + `}`, "application/json", "unknown", "duplicate_critical_field", "root.status", false},
		{"nested SSE status", strings.Replace(finalEvent("21"), `"status":"completed"`, `"status":"incomplete","Status":"completed"`, 1), "text/event-stream", "unknown", "duplicate_critical_field", "root.response.status", false},
		{"SSE type alias", strings.Replace(finalEvent("21"), `"type":"response.completed"`, `"type":"response.failed","Type":"response.completed"`, 1), "text/event-stream", "unknown", "duplicate_critical_field", "root.type", false},
		{"text alias", `{"status":"completed","output":` + strings.Replace(output, `"text":"21"`, `"text":"29","Text":"21"`, 1) + `}`, "application/json", "unknown", "duplicate_critical_field", "root.output[].content[].text", false},
		{"response ID alias", `{"id":"first","ID":"second","status":"completed","output":` + output + `}`, "application/json", "unknown", "duplicate_critical_field", "root.id", false},
		{"chat finish alias", `{"choices":[{"index":0,"message":{"content":"21"},"finish_reason":"length","Finish_Reason":"stop"}]}`, "application/json", "unknown", "duplicate_critical_field", "root.choices[].finish_reason", true},
		{"chat refusal alias", `{"choices":[{"index":0,"message":{"content":"21","refusal":"declined","Refusal":""},"finish_reason":"stop"}]}`, "application/json", "unknown", "duplicate_critical_field", "root.choices[].message.refusal", true},
		{"single case alias remains compatible", `{"Status":"completed","Output":` + output + `}`, "application/json", "smart", "correct_answer", "", false},
		{"distinct extension keys remain compatible", "data: {\"type\":\"response.auxiliary\",\"custom\":1,\"Custom\":2}\n\n" + finalEvent("21"), "text/event-stream", "smart", "correct_answer", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {tc.media}}, Body: io.NopCloser(strings.NewReader(tc.body))}, tc.chat, "http")
			if r.Status != tc.status || r.Reason != tc.reason || r.Diagnostic == nil || r.Diagnostic.Field != tc.field {
				t.Fatalf("%+v diagnostic=%+v", r, r.Diagnostic)
			}
		})
	}
}

func TestHTTPFinalMessageBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, output, status, reason, answer string }{
		{"independent final messages", `[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"2"}]},{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"1"}]}]`, "unknown", "conflicting_final_response", ""},
		{"one message split text", `[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"2"},{"type":"output_text","text":"1"}]}]`, "smart", "correct_answer", "21"},
		{"independent JSON fragments", `[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"{\"answer\":2"}]},{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"1}"}]}]`, "unknown", "conflicting_final_response", ""},
		{"one message split JSON", `[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"{\"answer\":2"},{"type":"output_text","text":"1}"}]}]`, "smart", "correct_answer", `{"answer":21}`},
		{"independent legacy messages", `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"2"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"1"}]}]`, "unknown", "conflicting_final_response", ""},
		{"empty message before answer", `[{"type":"message","role":"assistant","phase":"final_answer","content":[]},{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"21"}]}]`, "smart", "correct_answer", "21"},
	} {
		for _, media := range []string{"application/json", "text/event-stream"} {
			t.Run(tc.name+"/"+media, func(t *testing.T) {
				body := `{"id":"fixture","status":"completed","output":` + tc.output + `}`
				if media == "text/event-stream" {
					body = `data: {"type":"response.completed","response":` + body + "}\n\n"
				}
				r := ParseHTTP(&http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {media}}, Body: io.NopCloser(strings.NewReader(body))}, false, "http")
				if r.Status != tc.status || r.Reason != tc.reason || r.Answer != tc.answer {
					t.Fatalf("%+v diagnostic=%+v", r, r.Diagnostic)
				}
			})
		}
	}
}

func TestHTTPCompressedUpstreamRoundTrip(t *testing.T) {
	for _, encoding := range []string{"gzip", "deflate", "br", "zstd"} {
		t.Run(encoding, func(t *testing.T) {
			var encoded bytes.Buffer
			var writer io.WriteCloser
			switch encoding {
			case "gzip":
				writer = gzip.NewWriter(&encoded)
			case "deflate":
				writer = zlib.NewWriter(&encoded)
			case "br":
				writer = brotli.NewWriter(&encoded)
			case "zstd":
				var err error
				writer, err = zstd.NewWriter(&encoded)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := io.WriteString(writer, finalEvent("21")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Content-Encoding", encoding)
				_, _ = w.Write(encoded.Bytes())
			}))
			defer server.Close()
			response, err := server.Client().Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			if encoding == "gzip" && !response.Uncompressed {
				t.Fatal("expected transport gzip decompression")
			}
			result := ParseHTTP(response, false, "http")
			if result.Status != "smart" || result.NormalizedAnswer == nil || *result.NormalizedAnswer != "21" {
				t.Fatalf("%+v", result)
			}
		})
	}
}
