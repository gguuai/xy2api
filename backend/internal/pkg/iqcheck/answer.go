package iqcheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

type ExtractedAnswer struct {
	Value     string
	Format    string
	Reason    string
	Compliant bool
}

var numericAnswer = regexp.MustCompile(`^[+-]?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)
var answerMarkup = strings.NewReplacer("**", "", "__", "", "`", "", "$", "", "\\(", "", "\\)", "", "\\[", "", "\\]", "")
var conclusion = regexp.MustCompile(`(?i)(?:最终答案|最终结果|正确答案|答案|结论|最少(?:需要)?(?:取出|摸出|拿出|取|摸|拿)?|至少(?:需要)?(?:取出|摸出|拿出|取|摸|拿)?|the\s+(?:final\s+)?answer|minimum)[^0-9+\-\n。！？;；]{0,64}([+-]?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?)`)
var leadingNumber = regexp.MustCompile(`^\s*([+-]?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?)\s*(?:(?:个|颗)(?:糖果)?)?\s*[，,。.!！\n]`)
var boxedNumber = regexp.MustCompile(`\\boxed\{\s*([+-]?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?)\s*\}`)
var deduction = regexp.MustCompile(`(?:因此|所以|故|综上)[^0-9+\-\n。！？;；]{0,40}([+-]?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?)\s*(?:个|颗)`)
var standaloneNumber = regexp.MustCompile(`(?m)^\s*([+-]?[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?)\s*(?:个(?:糖果)?|颗(?:糖果)?)?\s*[。.!！]?\s*$`)
var ambiguity = regexp.MustCompile(`(?i)^(?:\s*(?:(?:个|颗)(?:\s*糖果)?)?\s*[,，:：]?\s*)(?:或|还是|或者|or\b|either\b|到[0-9]|[-~～][0-9])`)

// Bound exponents before exact rational parsing: upstream text is untrusted input.
func exactNumber(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if len(text) > 512 || !numericAnswer.MatchString(text) {
		return "", false
	}
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		exp, err := strconv.Atoi(text[i+1:])
		if err != nil || exp < -1024 || exp > 1024 {
			return "", false
		}
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok {
		return "", false
	}
	if value.IsInt() {
		return value.Num().String(), true
	}
	return value.RatString(), true
}

// Reject duplicate keys at every level, including nested explanation objects.
func uniqueJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 32 {
			return nil, errors.New("json_depth")
		}
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return token, nil
		}
		switch delimiter {
		case '{':
			object := map[string]any{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				name, ok := key.(string)
				if !ok {
					return nil, errors.New("json_key")
				}
				if _, exists := object[name]; exists {
					return nil, errors.New("duplicate_key")
				}
				value, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				object[name] = value
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return nil, errors.New("json_object")
			}
			return object, nil
		case '[':
			array := []any{}
			for decoder.More() {
				value, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return nil, errors.New("json_array")
			}
			return array, nil
		default:
			return nil, errors.New("json_delimiter")
		}
	}
	value, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing_json")
	}
	return value, nil
}

func ExtractAnswer(answer string) ExtractedAnswer {
	text := strings.TrimSpace(answer)
	if text == "" {
		return ExtractedAnswer{Reason: "empty_response"}
	}
	if len(text) > MaxAnswerBytes {
		return ExtractedAnswer{Reason: "answer_too_large"}
	}
	raw := text
	if strings.HasPrefix(text, "```") {
		first := strings.IndexByte(text, '\n')
		if first < 0 || !strings.HasSuffix(text, "```") {
			return ExtractedAnswer{Reason: "invalid_answer_structure"}
		}
		text = strings.TrimSpace(text[first+1 : len(text)-3])
	}
	if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
		invalid := ExtractedAnswer{Format: "json", Reason: "invalid_answer_structure"}
		value, err := uniqueJSON([]byte(text))
		if err != nil {
			return invalid
		}
		object, ok := value.(map[string]any)
		if !ok {
			return invalid
		}
		var number string
		numeric := false
		switch v := object["answer"].(type) {
		case json.Number:
			number = string(v)
			numeric = true
		case string:
			number = v
		default:
			return invalid
		}
		normalized, ok := exactNumber(number)
		if !ok {
			return invalid
		}
		// Explanations may contain intermediate numbers, but an explicit conflicting answer is invalid.
		for key, value := range object {
			if key == "answer" {
				continue
			}
			if explanation, ok := value.(string); ok {
				extracted := extractText(explanation)
				if extracted.Reason == "ambiguous_answer" || extracted.Value != "" && extracted.Value != normalized {
					return ExtractedAnswer{Format: "json", Reason: "ambiguous_answer"}
				}
			}
		}
		return ExtractedAnswer{Value: normalized, Format: "json", Compliant: raw == text && len(object) == 1 && numeric && !strings.Contains(normalized, "/")}
	}
	if value, ok := exactNumber(text); ok {
		return ExtractedAnswer{Value: value, Format: "number"}
	}
	return extractText(answerMarkup.Replace(text))
}

func AnswerIs21(answer string) bool { return ExtractAnswer(answer).Value == "21" }

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(strings.ToLower(text), value) {
			return true
		}
	}
	return false
}

func extractText(text string) ExtractedAnswer {
	result := ExtractedAnswer{Format: "text", Reason: "unparseable_answer"}
	type candidate struct {
		value                 string
		start, rank           int
		uncertain, correction bool
	}
	candidates := []candidate{}
	for _, pattern := range []*regexp.Regexp{conclusion, boxedNumber, deduction, leadingNumber, standaloneNumber} {
		for _, match := range pattern.FindAllStringSubmatchIndex(text, -1) {
			start := match[0]
			// Scope polarity to the current sentence, retaining quotes and counterexample context.
			sentenceStart := 0
			for offset, r := range text[:start] {
				if strings.ContainsRune("。！？\n;；,，", r) {
					sentenceStart = offset + utf8.RuneLen(r)
				}
			}
			prefix := text[sentenceStart:match[2]]
			suffix := text[match[3]:]
			sentenceEnd := strings.IndexAny(suffix, "。！？\n;；")
			tail := suffix
			if sentenceEnd >= 0 {
				tail = suffix[:sentenceEnd]
			}
			if containsAny(prefix, "有人认为", "有人说", "假设", "反例", "错误答案", "曾考虑", "例如", "比如", "不是", "并非", "不为", "不等于", "not ", "someone", "example", "\"", "“", "「") || containsAny(tail, "不成立", "不正确", "不是正确", "不是最终", "不作为答案") {
				continue
			}
			value, ok := exactNumber(text[match[2]:match[3]])
			if !ok {
				continue
			}
			rank := 1
			if pattern == conclusion || pattern == boxedNumber {
				rank = 2
			}
			if containsAny(prefix, "最终答案", "最终结果", "final answer") {
				rank = 3
			}
			correction := containsAny(prefix, "更正", "修正", "改为", "correction", "corrected")
			if correction {
				rank = 4
			}
			uncertain := containsAny(prefix, "可能", "也许", "不确定", "maybe", "either ") || ambiguity.MatchString(suffix) || containsAny(tail, "无法确定", "不确定", "只是中间")
			candidates = append(candidates, candidate{value, start, rank, uncertain, correction})
		}
	}
	best := 0
	for _, c := range candidates {
		if c.rank > best {
			best = c.rank
		}
	}
	lastCorrection := -1
	for _, c := range candidates {
		if c.rank == best && c.correction && c.start > lastCorrection {
			lastCorrection = c.start
		}
	}
	for _, c := range candidates {
		if c.rank != best || (lastCorrection >= 0 && c.start < lastCorrection) {
			continue
		}
		if c.uncertain || (result.Value != "" && result.Value != c.value) {
			return ExtractedAnswer{Format: "text", Reason: "ambiguous_answer"}
		}
		result.Value = c.value
	}
	if result.Value != "" {
		result.Reason = ""
	}
	return result
}
