package service

import (
	"bytes"
	"github.com/tidwall/gjson"
	"strings"
)

const SchedulingMetricVersion = "semantic-v1"

// A bounded incremental SSE decoder. Oversized data frames do not become valid
// latency samples. Existing protocol parsers continue to handle their payloads.
type semanticEventParser struct {
	pending     []byte
	data        []byte
	oversized   bool
	toolPending bool
	onFrame     func([]byte)
}

func (p *semanticEventParser) Feed(b []byte, emit func(bool, bool, bool)) {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			if len(p.pending)+len(b) <= openAIFirstOutputStageMaxBytes {
				p.pending = append(p.pending, b...)
			} else {
				p.oversized = true
				p.pending = nil
			}
			return
		}
		if !p.oversized && len(p.pending)+i <= openAIFirstOutputStageMaxBytes {
			p.pending = append(p.pending, b[:i]...)
		} else {
			p.oversized = true
		}
		line := bytes.TrimSuffix(p.pending, []byte{'\r'})
		b = b[i+1:]
		if len(line) == 0 && !p.oversized {
			p.emit(emit)
		} else if !p.oversized && bytes.HasPrefix(line, []byte("data:")) {
			value := bytes.TrimPrefix(line, []byte("data:"))
			value = bytes.TrimPrefix(value, []byte(" "))
			if len(p.data)+len(value) > openAIFirstOutputStageMaxBytes {
				p.oversized = true
				p.data = nil
			} else {
				if len(p.data) > 0 {
					p.data = append(p.data, '\n')
				}
				p.data = append(p.data, value...)
			}
		}
		if len(line) == 0 {
			p.oversized = false
			p.data = nil
		}
		p.pending = nil
	}
}
func (p *semanticEventParser) emit(emit func(bool, bool, bool)) {
	if len(p.data) == 0 {
		return
	}
	if p.onFrame != nil {
		p.onFrame(p.data)
	}
	s, a, t, tool := classifySemanticEvent(p.data)
	if tool {
		p.toolPending = true
	}
	blockStop := gjson.GetBytes(p.data, "type").String() == "content_block_stop"
	if (t || blockStop) && p.toolPending {
		s = true
		p.toolPending = false
	}
	if emit != nil {
		emit(s, a, t)
	}
}

// Returns semantic output, answer output, terminal success, and tool identity.
// Timing begins at content, never headers, heartbeats, roles or message_start.
func classifySemanticEvent(data []byte) (bool, bool, bool, bool) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		return false, false, true, false
	}
	if !gjson.ValidBytes(data) {
		return false, false, false, false
	}
	v := gjson.ParseBytes(data)
	// Gemini/Antigravity envelopes preserve the same decoded semantic content.
	if v.Get("response.candidates").Exists() {
		v = v.Get("response")
	}
	typ := v.Get("type").String()
	text := func(path string) bool { return len(v.Get(path).String()) > 0 }
	switch typ {
	case "response.output_text.delta", "response.refusal.delta":
		return text("delta"), text("delta"), false, false
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		return text("delta"), false, false, false
	case "response.function_call_arguments.delta":
		return text("delta"), false, false, false
	case "response.output_item.added":
		return false, false, false, v.Get("item.type").String() == "function_call" && text("item.name")
	case "response.output_item.done":
		if v.Get("item.type").String() == "function_call" && text("item.name") {
			return true, false, false, false
		}
	case "response.completed", "response.done":
		return false, false, true, false
	case "content_block_start":
		k := v.Get("content_block.type").String()
		if k == "text" {
			return text("content_block.text"), text("content_block.text"), false, false
		}
		if k == "thinking" {
			return text("content_block.thinking"), false, false, false
		}
		return false, false, false, k == "tool_use" && text("content_block.name")
	case "content_block_delta":
		k := v.Get("delta.type").String()
		if k == "text_delta" {
			return text("delta.text"), text("delta.text"), false, false
		}
		if k == "thinking_delta" {
			return text("delta.thinking"), false, false, false
		}
		if k == "input_json_delta" {
			return text("delta.partial_json"), false, false, false
		}
	case "content_block_stop":
		return false, false, false, false
	case "message_stop":
		return false, false, true, false
	}
	semantic, answer, terminal, tool := false, false, false, false
	for _, choice := range v.Get("choices").Array() {
		d := choice.Get("delta")
		if d.Get("content").String() != "" || d.Get("refusal").String() != "" {
			semantic = true
			answer = true
		}
		if d.Get("reasoning_content").String() != "" || d.Get("reasoning").String() != "" {
			semantic = true
		}
		for _, call := range d.Get("tool_calls").Array() {
			if call.Get("function.arguments").String() != "" {
				semantic = true
			}
			if call.Get("function.name").String() != "" {
				tool = true
			}
		}
		if choice.Get("finish_reason").String() != "" {
			terminal = true
		}
	}
	for _, candidate := range v.Get("candidates").Array() {
		for _, part := range candidate.Get("content.parts").Array() {
			if part.Get("text").String() != "" {
				semantic = true
				if !part.Get("thought").Bool() {
					answer = true
				}
			}
			if part.Get("functionCall.name").String() != "" {
				semantic = true
			}
			if part.Get("inlineData.data").String() != "" {
				semantic = true
				answer = true
			}
		}
		if candidate.Get("finishReason").String() != "" {
			terminal = true
		}
	}
	// Do not guess timing from arbitrary provider status/error events.
	if strings.HasSuffix(typ, ".failed") || typ == "error" {
		terminal = false
	}
	return semantic, answer, terminal, tool
}
