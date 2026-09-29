package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// Keep selected scalars, never another copy of prompts or tool schemas.
// The handler remains responsible for protocol validation.
var schedulingMetadataPaths = map[string]map[string]string{
	"root":            {"model": "scalar", "stream": "scalar", "reasoning": "reasoning", "reasoning_effort": "scalar", "thinking": "thinking", "output_config": "output", "generationConfig": "generation", "generation_config": "generation", "tools": "tools"},
	"outbound":        {"model": "scalar", "stream": "scalar"},
	"reasoning":       {"effort": "scalar"},
	"thinking":        {"type": "scalar", "budget_tokens": "scalar"},
	"output":          {"effort": "scalar"},
	"generation":      {"thinkingConfig": "gemini_thinking", "thinking_config": "gemini_thinking"},
	"gemini_thinking": {"thinkingLevel": "scalar", "thinking_level": "scalar", "thinkingBudget": "scalar", "thinking_budget": "scalar"},
	"tool":            {"type": "scalar"},
}
var errSchedulingMetadata = errors.New("invalid scheduling metadata")

type schedulingMetadataScanner struct {
	r           *bufio.Reader
	replaySafe  bool
	nativeTools bool
}

func (s *schedulingMetadataScanner) space() (byte, error) {
	for {
		b, e := s.r.ReadByte()
		if e != nil {
			return 0, e
		}
		if b != ' ' && b != '\n' && b != '\r' && b != '\t' {
			return b, nil
		}
	}
}

// Quoted values may be arbitrarily long. Selected scalars retain at most 4 KiB;
// unselected strings are validated and discarded as they arrive.
func (s *schedulingMetadataScanner) quoted(capture bool) (json.RawMessage, error) {
	var out []byte
	if capture {
		out = append(out, '"')
	}
	large := false
	for {
		b, e := s.r.ReadByte()
		if e != nil {
			return nil, e
		}
		if b < 0x20 {
			return nil, errSchedulingMetadata
		}
		if capture && !large {
			if len(out) < 4096 {
				out = append(out, b)
			} else {
				large = true
				out = nil
			}
		}
		if b == '"' {
			if large {
				return json.RawMessage("null"), nil
			}
			return out, nil
		}
		if b == '\\' {
			b, e = s.r.ReadByte()
			if e != nil {
				return nil, e
			}
			if capture && !large {
				out = append(out, b)
			}
			switch b {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				for i := 0; i < 4; i++ {
					h, e := s.r.ReadByte()
					if e != nil {
						return nil, e
					}
					if (h < '0' || h > '9') && (h < 'a' || h > 'f') && (h < 'A' || h > 'F') {
						return nil, errSchedulingMetadata
					}
					if capture && !large {
						out = append(out, h)
					}
				}
			default:
				return nil, errSchedulingMetadata
			}
		}
	}
}
func (s *schedulingMetadataScanner) value(path string, depth int) (json.RawMessage, error) {
	if depth > 128 {
		return nil, errSchedulingMetadata
	}
	b, e := s.space()
	if e != nil {
		return nil, e
	}
	if b == '"' {
		return s.quoted(path != "")
	}
	if b == '{' {
		fields := map[string]json.RawMessage{}
		next, e := s.space()
		if e != nil {
			return nil, e
		}
		if next == '}' {
			if path == "" {
				return nil, nil
			}
			return json.RawMessage("{}"), nil
		}
		_ = s.r.UnreadByte()
		for {
			q, e := s.space()
			if e != nil || q != '"' {
				return nil, errSchedulingMetadata
			}
			raw, e := s.quoted(path != "")
			if e != nil {
				return nil, e
			}
			var key string
			if path != "" {
				_ = json.Unmarshal(raw, &key)
			}
			colon, e := s.space()
			if e != nil || colon != ':' {
				return nil, errSchedulingMetadata
			}
			child := schedulingMetadataPaths[path][key]
			val, e := s.value(child, depth+1)
			if e != nil {
				return nil, e
			}
			if child != "" {
				fields[key] = val
			}
			next, e = s.space()
			if e != nil {
				return nil, e
			}
			if next == '}' {
				break
			}
			if next != ',' {
				return nil, errSchedulingMetadata
			}
		}
		if path == "" {
			return nil, nil
		}
		return json.Marshal(fields)
	}
	if b == '[' {
		next, e := s.space()
		if e != nil {
			return nil, e
		}
		if next == ']' {
			return json.RawMessage("[]"), nil
		}
		_ = s.r.UnreadByte()
		for {
			child := ""
			if path == "tools" {
				child = "tool"
				if s.nativeTools {
					s.replaySafe = false
				}
			}
			raw, e := s.value(child, depth+1)
			if e != nil {
				return nil, e
			}
			if child == "tool" {
				var tool struct{ Type string }
				if json.Unmarshal(raw, &tool) != nil || (tool.Type != "function" && tool.Type != "custom") {
					s.replaySafe = false
				}
			}
			next, e = s.space()
			if e != nil {
				return nil, e
			}
			if next == ']' {
				break
			}
			if next != ',' {
				return nil, errSchedulingMetadata
			}
		}
		return json.RawMessage("[]"), nil
	}
	raw := []byte{b}
	for {
		b, e = s.r.ReadByte()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		if b == ',' || b == '}' || b == ']' || b == ' ' || b == '\n' || b == '\r' || b == '\t' {
			_ = s.r.UnreadByte()
			break
		}
		if len(raw) >= 128 {
			return nil, errSchedulingMetadata
		}
		raw = append(raw, b)
	}
	if !json.Valid(raw) {
		return nil, errSchedulingMetadata
	}
	if path == "tools" && !bytes.Equal(raw, []byte("null")) {
		s.replaySafe = false
	}
	if path == "" {
		return nil, nil
	}
	return raw, nil
}
func readSchedulingMetadata(reader io.Reader, protocol, root string) (map[string]json.RawMessage, bool, bool) {
	s := schedulingMetadataScanner{r: bufio.NewReaderSize(reader, 4096), replaySafe: true, nativeTools: protocol == "gemini"}
	raw, err := s.value(root, 0)
	if err != nil {
		return nil, false, false
	}
	if _, err = s.space(); err != io.EOF {
		return nil, false, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, false, false
	}
	if raw, ok := fields["tools"]; ok && string(raw) != "[]" && string(raw) != "null" {
		s.replaySafe = false
	}
	return fields, s.replaySafe, true
}

// ReadControlledOutboundMetadata scans a separate body reader. It never closes,
// replaces or consumes the live body; fields may appear beyond 256 KiB.
func ReadControlledOutboundMetadata(reader io.Reader) (model string, stream bool, valid bool) {
	fields, _, valid := readSchedulingMetadata(reader, "", "outbound")
	if !valid {
		return "", false, false
	}
	if raw, ok := fields["model"]; ok && json.Unmarshal(raw, &model) != nil {
		return "", false, false
	}
	if raw, ok := fields["stream"]; ok && json.Unmarshal(raw, &stream) != nil {
		return "", false, false
	}
	return model, stream, true
}
