package iqcheck

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

const MaxEventBytes = 2 * 1024 * 1024
const MaxEvents = 4096

var errBodyLimit = errors.New("response_too_large")

type countedReader struct {
	r io.Reader
	n int64
}

func (r *countedReader) Read(b []byte) (int, error) {
	if r.n >= MaxResponseBytes+1 {
		return 0, errBodyLimit
	}
	if len(b) > MaxResponseBytes+1-int(r.n) {
		b = b[:MaxResponseBytes+1-int(r.n)]
	}
	n, e := r.r.Read(b)
	r.n += int64(n)
	if r.n > MaxResponseBytes {
		return n, errBodyLimit
	}
	return n, e
}

type parseFailure struct {
	code, field string
	offset      int64
}

func (e *parseFailure) Error() string { return e.code }
func failure(code string) error       { return &parseFailure{code: code} }

// Validate JSON without storing values. Paths contain only known schema names,
// never arbitrary upstream keys. Unknown event payloads need not match Response.
func validateEvent(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	known := map[string]bool{"output_index": true, "item": true, "type": true, "response": true, "status": true, "output": true, "content": true, "text": true, "answer": true, "role": true, "phase": true, "error": true, "model": true, "choices": true, "delta": true, "message": true, "finish_reason": true, "index": true, "id": true, "refusal": true}
	critical := map[string]bool{"output_index": true, "item": true, "type": true, "response": true, "status": true, "output": true, "content": true, "text": true, "answer": true, "role": true, "phase": true, "error": true, "choices": true, "delta": true, "message": true, "finish_reason": true, "index": true, "model": true, "id": true, "refusal": true}
	var walk func(int, string) error
	walk = func(depth int, path string) error {
		if depth > 32 {
			return &parseFailure{"invalid_event_json", path, dec.InputOffset()}
		}
		tok, err := dec.Token()
		if err != nil {
			return &parseFailure{"invalid_event_json", path, dec.InputOffset()}
		}
		d, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch d {
		case '{':
			keys := map[string]bool{}
			for dec.More() {
				k, e := dec.Token()
				if e != nil {
					return failure("invalid_event_json")
				}
				key, ok := k.(string)
				if !ok {
					return failure("invalid_event_json")
				}
				// Match encoding/json's case-insensitive field lookup before checking duplicates.
				if !known[key] {
					for name := range known {
						if strings.EqualFold(key, name) {
							key = name
							break
						}
					}
				}
				field := "extension"
				if known[key] {
					field = key
				}
				p := path + "." + field
				if keys[key] {
					code := "duplicate_event_field"
					if critical[key] {
						code = "duplicate_critical_field"
					}
					return &parseFailure{code, p, dec.InputOffset()}
				}
				keys[key] = true
				if e = walk(depth+1, p); e != nil {
					return e
				}
			}
			end, e := dec.Token()
			if e != nil || end != json.Delim('}') {
				return failure("invalid_event_json")
			}
		case '[':
			for dec.More() {
				if e := walk(depth+1, path+"[]"); e != nil {
					return e
				}
			}
			end, e := dec.Token()
			if e != nil || end != json.Delim(']') {
				return failure("invalid_event_json")
			}
		default:
			return failure("invalid_event_json")
		}
		return nil
	}
	if err := walk(0, "root"); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return &parseFailure{"invalid_event_json", "root", dec.InputOffset()}
	}
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return failure("invalid_event_shape")
	}
	return nil
}

func decode(raw []byte, v any, field string) error {
	if err := json.Unmarshal(raw, v); err != nil {
		offset := int64(0)
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			offset = typeErr.Offset
		}
		return &parseFailure{"invalid_event_shape", field, offset}
	}
	return nil
}

type streamParser struct {
	chat, sse, completed, done bool
	answer, model, responseID  string
	doneItems                  map[int]json.RawMessage
	itemIDs                    map[string]int
	d                          *Diagnostic
}

func (p *streamParser) consume(raw []byte, eventName string) error {
	p.d.EventType = safeToken(eventName, 96)
	if err := validateEvent(raw); err != nil {
		return err
	}
	var env struct {
		Type     string          `json:"type"`
		Error    json.RawMessage `json:"error"`
		Response json.RawMessage `json:"response"`
	}
	if err := decode(raw, &env, "root"); err != nil {
		return err
	}
	p.d.EventType = safeToken(env.Type, 96)
	if eventName != "" && eventName != "message" && env.Type != "" && eventName != env.Type {
		return failure("event_type_mismatch")
	}
	if env.Type == "" && eventName != "message" {
		env.Type = eventName
		p.d.EventType = safeToken(eventName, 96)
	}
	if p.done {
		return failure("data_after_done")
	}
	if env.Error != nil && string(env.Error) != "null" {
		return failure(ReadUpstreamError(raw, p.d))
	}
	if isErrorEnvelope(raw) {
		return failure(ReadUpstreamError(raw, p.d))
	}
	if p.chat {
		return p.consumeChat(raw)
	}
	if !p.sse {
		return p.terminal(raw)
	}
	switch env.Type {
	case "error", "response.failed":
		return failure(ReadUpstreamError(raw, p.d))
	case "response.incomplete":
		return failure("incomplete_response")
	case "response.output_item.done":
		return p.collectDone(raw)
	case "response.completed", "response.done":
		if len(env.Response) == 0 || string(env.Response) == "null" {
			return failure("invalid_terminal_response")
		}
		return p.terminal(env.Response)
	case "":
		return failure("invalid_event_shape")
	default:
		// Auxiliary events and deltas never supply a final answer. Their payloads
		// evolve independently of the terminal Response schema.
		return nil
	}
}

// Only completed message envelopes are retained; reasoning payloads are discarded.
func (p *streamParser) collectDone(raw []byte) error {
	var e struct {
		Index *int            `json:"output_index"`
		Item  json.RawMessage `json:"item"`
	}
	if err := decode(raw, &e, "output_item.done"); err != nil {
		return err
	}
	if e.Index == nil || *e.Index < 0 || *e.Index >= MaxEvents {
		return failure("invalid_output_identity")
	}
	var item struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		Role   string `json:"role"`
		Status string `json:"status"`
	}
	if err := decode(e.Item, &item, "item"); err != nil {
		return err
	}
	if item.Type != "message" {
		p.d.ignoreItem(item.Type)
		return nil
	}
	if item.ID == "" || len(item.ID) > 256 {
		return failure("invalid_output_identity")
	}
	if item.Role != "assistant" || item.Status != "completed" {
		return failure("invalid_completed_message")
	}
	if p.doneItems == nil {
		p.doneItems = map[int]json.RawMessage{}
		p.itemIDs = map[string]int{}
	}
	if index, exists := p.itemIDs[item.ID]; exists && index != *e.Index {
		return failure("conflicting_output_identity")
	}
	if previous, exists := p.doneItems[*e.Index]; exists {
		var a, b any
		_ = json.Unmarshal(previous, &a)
		_ = json.Unmarshal(e.Item, &b)
		aa, _ := json.Marshal(a)
		bb, _ := json.Marshal(b)
		if !bytes.Equal(aa, bb) {
			return failure("conflicting_final_response")
		}
		return nil
	}
	if p.completed {
		return failure("conflicting_final_response")
	}
	p.doneItems[*e.Index] = append(json.RawMessage(nil), e.Item...)
	p.itemIDs[item.ID] = *e.Index
	p.d.DoneMessages = len(p.doneItems)
	return nil
}

func (p *streamParser) terminal(raw []byte) error {
	var r struct {
		ID     string            `json:"id"`
		Status string            `json:"status"`
		Model  string            `json:"model"`
		Output []json.RawMessage `json:"output"`
		Error  json.RawMessage   `json:"error"`
	}
	if err := decode(raw, &r, "response"); err != nil {
		return err
	}
	if r.Error != nil && string(r.Error) != "null" {
		return failure(ReadUpstreamError(raw, p.d))
	}
	if r.Status != "completed" {
		return failure("incomplete_response")
	}
	ReadUsage(raw, p.d)
	if len(r.Model) > 256 {
		return failure("metadata_too_large")
	}
	p.d.TerminalItems = len(r.Output)
	p.d.AnswerSource = "terminal_output"
	// Identity and content must agree wherever the terminal repeats a done item.
	indexes := make([]int, 0, len(p.doneItems))
	for index := range p.doneItems {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		done := p.doneItems[index]
		var d struct {
			ID      string          `json:"id"`
			Role    string          `json:"role"`
			Phase   string          `json:"phase"`
			Status  string          `json:"status"`
			Content json.RawMessage `json:"content"`
		}
		if err := decode(done, &d, "item"); err != nil {
			return err
		}
		if d.Role != "assistant" || d.Status != "completed" {
			return failure("incomplete_response")
		}
		found := false
		for _, terminal := range r.Output {
			var t struct {
				ID      string          `json:"id"`
				Type    string          `json:"type"`
				Role    string          `json:"role"`
				Phase   string          `json:"phase"`
				Status  string          `json:"status"`
				Content json.RawMessage `json:"content"`
			}
			if err := decode(terminal, &t, "response.output[]"); err != nil {
				return err
			}
			if t.ID != d.ID {
				continue
			}
			found = true
			var dc, tc any
			_ = json.Unmarshal(d.Content, &dc)
			_ = json.Unmarshal(t.Content, &tc)
			db, _ := json.Marshal(dc)
			tb, _ := json.Marshal(tc)
			if t.Type != "message" || d.Role != t.Role || d.Phase != t.Phase || t.Status != "" && d.Status != t.Status || !bytes.Equal(db, tb) {
				return failure("conflicting_final_response")
			}
		}
		if !found {
			r.Output = append(r.Output, done)
			p.d.AnswerSource = "completed_items"
		}
	}
	var answer string
	finalMessages := 0
	for _, rawItem := range r.Output {
		var item struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Phase   string          `json:"phase"`
			Status  string          `json:"status"`
			Content json.RawMessage `json:"content"`
		}
		if err := decode(rawItem, &item, "response.output[]"); err != nil {
			return err
		}
		if item.Type != "message" || item.Role != "assistant" || item.Phase != "" && item.Phase != "final_answer" {
			kind := item.Type
			if item.Type == "message" {
				if item.Role != "assistant" {
					kind = "non_assistant"
				} else {
					kind = "commentary"
				}
			}
			p.d.ignoreItem(kind)
			continue
		}
		finalMessages++
		if item.Status != "" && item.Status != "completed" {
			return failure("incomplete_response")
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := decode(item.Content, &parts, "response.output[].content"); err != nil {
			return err
		}
		var message strings.Builder
		for _, part := range parts {
			if part.Type == "refusal" {
				return failure("refusal")
			}
			if part.Type == "output_text" {
				_, _ = message.WriteString(part.Text)
			}
		}
		if message.Len() > 0 {
			// Content parts share a message; independent final messages do not.
			if answer != "" {
				return failure("conflicting_final_response")
			}
			answer = message.String()
		}
	}
	if len(answer) > MaxAnswerBytes {
		p.d.LimitKind, p.d.LimitBytes = "answer_bytes", MaxAnswerBytes
		return failure("answer_too_large")
	}
	if p.completed && (p.answer != answer || p.responseID != r.ID || p.model != r.Model) {
		return failure("conflicting_final_response")
	}
	ReadUsage(raw, p.d)
	p.answer, p.model, p.responseID, p.completed = answer, r.Model, r.ID, true
	if finalMessages == 0 {
		p.d.Code = "missing_final_message"
	} else if answer == "" {
		p.d.Code = "empty_final_text"
	}
	return nil
}

func (p *streamParser) consumeChat(raw []byte) error {
	var r struct {
		Model   string `json:"model"`
		Choices []struct {
			Index  int     `json:"index"`
			Finish *string `json:"finish_reason"`
			Delta  struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"delta"`
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := decode(raw, &r, "choices"); err != nil {
		return err
	}
	ReadUsage(raw, p.d)
	if len(r.Model) > 256 {
		return failure("metadata_too_large")
	}
	if r.Model != "" {
		p.model = r.Model
	}
	for _, c := range r.Choices {
		if c.Index != 0 {
			continue
		}
		if c.Delta.Refusal != "" || c.Message.Refusal != "" {
			return failure("refusal")
		}
		text := c.Message.Content
		if p.sse {
			text = c.Delta.Content
		}
		if p.completed && (text != "" || c.Finish != nil) {
			return failure("conflicting_final_response")
		}
		p.answer += text
		if len(p.answer) > MaxAnswerBytes {
			p.d.LimitKind, p.d.LimitBytes = "answer_bytes", MaxAnswerBytes
			return failure("answer_too_large")
		}
		if c.Finish != nil {
			if *c.Finish != "stop" {
				return failure("incomplete_response")
			}
			p.completed = true
		}
	}
	return nil
}

// Parse keeps the established explicit-format API for callers and fixtures.
func Parse(body io.Reader, sse, chat bool) Result {
	d := &Diagnostic{ParserVersion: ParserVersion, Stage: "decode", Protocol: "responses"}
	if chat {
		d.Protocol = "chat_completions"
	}
	return parse(body, sse, chat, d)
}

func parse(body io.Reader, sse, chat bool, d *Diagnostic) Result {
	reader := &countedReader{r: body}
	p := streamParser{chat: chat, sse: sse, d: d}
	finish := func(err error) Result {
		d.BytesRead = reader.n
		if err != nil {
			d.Code = "response_read_failed"
			var f *parseFailure
			var networkError net.Error
			switch {
			case errors.As(err, &f):
				d.Code = f.code
				d.Field = f.field
				d.Offset = f.offset
			case errors.Is(err, errBodyLimit):
				d.Code = "response_too_large"
				d.LimitKind, d.LimitBytes = "response_bytes", MaxResponseBytes
			case errors.Is(err, context.DeadlineExceeded), errors.As(err, &networkError) && networkError.Timeout():
				d.Code = "timeout"
				d.Stage = "read"
			case errors.Is(err, context.Canceled):
				d.Code = "interrupted"
				d.Stage = "read"
			default:
				d.Stage = "read"
			}
			r := Unknown(d.Code)
			r.Diagnostic = d.Bounded()
			return r
		}
		if !p.completed {
			return finishUnknown(d, "incomplete_response")
		}
		d.Stage = "grade"
		if p.answer == "" && d.Code != "" {
			return finishUnknown(d, d.Code)
		}
		r := Grade(p.answer)
		d.Code = r.Reason
		r.ReportedModel = p.model
		r.Diagnostic = d.Bounded()
		return r
	}
	if !sse {
		raw, err := io.ReadAll(reader)
		if err != nil {
			return finish(err)
		}
		return finish(p.consume(raw, ""))
	}
	br := bufio.NewReaderSize(reader, 4096)
	var data []string
	eventName := ""
	frameBytes := 0
	events := 0
	flush := func() error {
		if len(data) == 0 {
			eventName = ""
			frameBytes = 0
			return nil
		}
		events++
		d.EventIndex = events
		if events > MaxEvents {
			return failure("too_many_events")
		}
		joined := strings.Join(data, "\n")
		data = nil
		frameBytes = 0
		name := eventName
		eventName = ""
		if joined == "[DONE]" {
			if !p.completed {
				return failure("incomplete_response")
			}
			p.done = true
			return nil
		}
		return p.consume([]byte(joined), name)
	}
	var line []byte
	for {
		part, prefix, err := br.ReadLine()
		line = append(line, part...)
		if len(line)+frameBytes > MaxEventBytes {
			d.LimitKind, d.LimitBytes = "event_bytes", MaxEventBytes
			return finish(failure("event_too_large"))
		}
		if err != nil {
			if err == io.EOF {
				if e := flush(); e != nil {
					return finish(e)
				}
				return finish(nil)
			}
			return finish(err)
		}
		if prefix {
			continue
		}
		s := string(line)
		line = nil
		if s == "" {
			if e := flush(); e != nil {
				return finish(e)
			}
			continue
		}
		frameBytes += len(s) + 1
		if strings.HasPrefix(s, ":") {
			continue
		}
		field, value, _ := strings.Cut(s, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			data = append(data, value)
		case "event":
			eventName = value
		}
	}
}

func finishUnknown(d *Diagnostic, code string) Result {
	d.Code = code
	r := Unknown(code)
	r.Diagnostic = d.Bounded()
	return r
}

// ParseHTTP resolves media type once and parses the decoded response stream.
// Transport normally decompresses; bounded decoding covers plugin responses too.
func ParseHTTP(resp *http.Response, chat bool, transport string) (result Result) {
	started := time.Now()
	defer func() {
		if result.Diagnostic != nil {
			result.Diagnostic.TotalMS = time.Since(started).Milliseconds()
		}
	}()
	d := &Diagnostic{ParserVersion: ParserVersion, Stage: "format", Protocol: "responses", Transport: transport}
	if chat {
		d.Protocol = "chat_completions"
	}
	if resp == nil || resp.Body == nil {
		return finishUnknown(d, "response_read_failed")
	}
	d.RetryVisibility = "single_application_call"
	if transport == "plugin" {
		d.RetryVisibility = "plugin_attempts_unknown"
	}
	d.RetryAfter, d.RetryAfterUnbounded = RetryAfter(resp.Header.Get("Retry-After"), time.Now().UTC())
	d.HTTPStatus = resp.StatusCode
	d.RequestID = resp.Header.Get("X-Request-Id")
	d.Encoding = strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	d.MediaType = media
	if resp.StatusCode == http.StatusOK && err != nil && resp.Header.Get("Content-Type") != "" {
		return finishUnknown(d, "response_format_mismatch")
	}
	var body io.Reader = resp.Body
	switch d.Encoding {
	case "", "identity":
	case "gzip":
		z, e := gzip.NewReader(body)
		if e != nil {
			return finishUnknown(d, "response_decompression_failed")
		}
		defer func() { _ = z.Close() }()
		body = z
	case "br":
		body = brotli.NewReader(body)
	case "zstd":
		z, e := zstd.NewReader(body, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(8<<20))
		if e != nil {
			return finishUnknown(d, "response_decompression_failed")
		}
		defer z.Close()
		body = z
	case "deflate":
		z, e := zlib.NewReader(body)
		if e != nil {
			return finishUnknown(d, "response_decompression_failed")
		}
		defer func() { _ = z.Close() }()
		body = z
	default:
		return finishUnknown(d, "unsupported_content_encoding")
	}
	if resp.StatusCode != http.StatusOK {
		d.Stage = "http"
		reason := httpCode(resp.StatusCode)
		if resp.StatusCode == http.StatusPaymentRequired {
			reason = "quota_exhausted"
		}
		raw, e := io.ReadAll(io.LimitReader(body, 8193))
		if e == nil {
			if parsed := ReadUpstreamError(raw, d); parsed != "upstream_error" {
				reason = parsed
			}
		}
		return finishUnknown(d, reason)
	}
	if media == "text/html" {
		return finishUnknown(d, "upstream_html_response")
	}
	if media != "" && media != "application/json" && !strings.HasSuffix(media, "+json") && media != "text/event-stream" && media != "application/octet-stream" && media != "text/plain" {
		return finishUnknown(d, "unsupported_media_type")
	}
	br := bufio.NewReaderSize(body, 4096)
	// Read only through the first non-whitespace byte before choosing a decoder;
	// waiting for a fixed-size Peek would delay short streaming responses.
	var prefix []byte
	for len(prefix) < 4096 {
		b, e := br.ReadByte()
		if e != nil {
			d.Stage = "read"
			var networkError net.Error
			if e == io.EOF && len(prefix) == 0 {
				return finishUnknown(d, "zero_byte_response")
			}
			if errors.Is(e, context.DeadlineExceeded) || errors.As(e, &networkError) && networkError.Timeout() {
				return finishUnknown(d, "timeout")
			}
			if errors.Is(e, context.Canceled) {
				return finishUnknown(d, "interrupted")
			}
			if d.Encoding != "" && d.Encoding != "identity" {
				return finishUnknown(d, "response_decompression_failed")
			}
			return finishUnknown(d, "response_read_failed")
		}
		if len(prefix) == 0 {
			d.FirstByteMS = time.Since(started).Milliseconds()
		}
		prefix = append(prefix, b)
		if !strings.ContainsRune(" \r\n\t", rune(b)) {
			break
		}
	}
	first := bytes.TrimSpace(prefix)
	if len(first) == 0 {
		return finishUnknown(d, "response_format_mismatch")
	}
	var sse bool
	switch first[0] {
	case '<':
		return finishUnknown(d, "upstream_html_response")
	case '{':
		sse = false
	case ':', 'd', 'e', 'i', 'r':
		sse = true
	default:
		return finishUnknown(d, "response_format_mismatch")
	}
	d.FormatDetected = (sse && media != "text/event-stream") || (!sse && media != "application/json")
	d.Stage = "decode"
	result = parse(io.MultiReader(bytes.NewReader(prefix), br), sse, chat, d)
	if d.Encoding != "" && d.Encoding != "identity" && result.Reason == "response_read_failed" {
		result = finishUnknown(result.Diagnostic, "response_decompression_failed")
	}
	return result
}

func httpCode(status int) string {
	// No upstream error message/body is retained here.
	return "http_" + strconv.Itoa(status)
}

// ValidateResponseEvent rejects ambiguous or damaged protocol JSON without grading.
func ValidateResponseEvent(raw []byte) error { return validateEvent(raw) }
