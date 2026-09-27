package iqcheck

import (
	"encoding/json"
	"strings"
	"time"
)

const ParserVersion = "iq-response-v5"

// Diagnostic never contains response text, reasoning, credentials or raw errors.
// Fixed field lengths keep its JSON representation below the database's 4 KiB cap.
type Diagnostic struct {
	LimitKind           string     `json:"limit_kind,omitempty"`
	LimitBytes          int        `json:"limit_bytes,omitempty"`
	Category            string     `json:"category,omitempty"`
	StateProtection     string     `json:"state_protection,omitempty"`
	DoneMessages        int        `json:"done_messages,omitempty"`
	TerminalItems       int        `json:"terminal_items,omitempty"`
	IgnoredItems        int        `json:"ignored_items,omitempty"`
	IgnoredTypes        string     `json:"ignored_types,omitempty"`
	AnswerSource        string     `json:"answer_source,omitempty"`
	FirstByteMS         int64      `json:"first_byte_ms"`
	TotalMS             int64      `json:"total_ms"`
	ErrorCode           string     `json:"error_code,omitempty"`
	ErrorType           string     `json:"error_type,omitempty"`
	RetryAfter          *time.Time `json:"retry_after,omitempty"`
	RetryAfterUnbounded bool       `json:"retry_after_unbounded,omitempty"`
	InputTokens         int64      `json:"input_tokens,omitempty"`
	OutputTokens        int64      `json:"output_tokens,omitempty"`
	ReasoningTokens     int64      `json:"reasoning_tokens,omitempty"`
	RetryVisibility     string     `json:"retry_visibility,omitempty"`

	ParserVersion  string `json:"parser_version"`
	Stage          string `json:"stage"`
	Code           string `json:"code,omitempty"`
	HTTPStatus     int    `json:"http_status,omitempty"`
	MediaType      string `json:"media_type,omitempty"`
	Encoding       string `json:"content_encoding,omitempty"`
	Protocol       string `json:"protocol,omitempty"`
	Transport      string `json:"transport,omitempty"`
	FormatDetected bool   `json:"format_detected,omitempty"`
	EventType      string `json:"event_type,omitempty"`
	EventIndex     int    `json:"event_index,omitempty"`
	Field          string `json:"field,omitempty"`
	Offset         int64  `json:"offset,omitempty"`
	BytesRead      int64  `json:"bytes_read,omitempty"`
	RequestID      string `json:"request_id,omitempty"`
}

func safeToken(s string, limit int) string {
	if len(s) > limit {
		s = s[:limit]
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-/[]", r) {
			return r
		}
		return '_'
	}, s)
}

func (d *Diagnostic) Bounded() *Diagnostic {
	if d == nil {
		return nil
	}
	v := *d
	switch v.LimitKind {
	case "response_bytes", "event_bytes", "answer_bytes":
	default:
		v.LimitKind, v.LimitBytes = "", 0
	}
	v.Category = FailureCategory(v.Code)
	if v.StateProtection != "protected" && v.StateProtection != "unprotected" && v.StateProtection != "unmanaged" {
		v.StateProtection = ""
	}
	v.AnswerSource = safeToken(v.AnswerSource, 32)
	v.IgnoredTypes = safeToken(v.IgnoredTypes, 128)
	v.ErrorCode = safeToken(v.ErrorCode, 64)
	v.ErrorType = safeToken(v.ErrorType, 64)
	v.RetryVisibility = safeToken(v.RetryVisibility, 32)
	v.ParserVersion = safeToken(v.ParserVersion, 32)
	v.Stage = safeToken(v.Stage, 32)
	v.Code = safeToken(v.Code, 64)
	v.MediaType = safeToken(v.MediaType, 96)
	v.Encoding = safeToken(v.Encoding, 32)
	v.Protocol = safeToken(v.Protocol, 32)
	v.Transport = safeToken(v.Transport, 32)
	v.EventType = safeToken(v.EventType, 96)
	v.Field = safeToken(v.Field, 128)
	v.RequestID = safeToken(v.RequestID, 128)
	return &v
}

func (d *Diagnostic) ignoreItem(kind string) {
	d.IgnoredItems++
	switch kind {
	case "reasoning", "commentary", "non_assistant", "function_call", "message":
	default:
		kind = "other"
	}
	for _, prior := range strings.Split(d.IgnoredTypes, "/") {
		if prior == kind {
			return
		}
	}
	if d.IgnoredTypes != "" {
		d.IgnoredTypes += "/"
	}
	d.IgnoredTypes += kind
}

func (d *Diagnostic) JSON() []byte {
	if d == nil {
		return []byte("null")
	}
	b, _ := json.Marshal(d.Bounded())
	return b
}
