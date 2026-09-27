// Package iqcheck implements the single candy-question check, not a model grader.
package iqcheck

import (
	"github.com/liulixin-lex/xy2api/internal/domain"
)

const Model = domain.DefaultIQModel
const Effort = domain.DefaultIQEffort
const PromptVersion = "candy-v2"
const GraderVersion = "candy-grader-v2"
const OutputInstruction = `请解答用户的题目。输出格式统一为一个简洁 JSON 对象，唯一字段为 answer，值为最终整数答案。不要输出解释、Markdown 或其他字段。`

// Reasoning and completed envelopes may repeat large payloads. Bound the wire
// stream separately from retained assistant text, which stays capped at 64 KiB.
const MaxResponseBytes = 8 * 1024 * 1024
const MaxAnswerBytes = 64 * 1024
const Prompt = `在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）
苹果味 桃子味 西瓜味
圆形 7 9 8
五角星形 7 6 4
只输出一个 JSON 对象，仅含整数 answer，不输出解释或 Markdown。`

type Result struct {
	StateFingerprint string `json:"-"`
	StateRevision    string `json:"-"`
	StateTicketID    string `json:"-"`
	StateModel       string `json:"-"`

	Diagnostic       *Diagnostic `json:"diagnostic,omitempty"`
	Status           string      `json:"status"`
	Answer           string      `json:"answer"`
	Reason           string      `json:"reason,omitempty"`
	NormalizedAnswer *string     `json:"normalized_answer,omitempty"`
	AnswerFormat     string      `json:"answer_format,omitempty"`
	FormatCompliant  bool        `json:"format_compliant"`
	ReportedModel    string      `json:"reported_model,omitempty"`
}

func Unknown(reason string) Result { return Result{Status: "unknown", Reason: reason} }

func Grade(answer string) Result {
	if len(answer) > MaxAnswerBytes {
		return Unknown("answer_too_large")
	}
	extracted := ExtractAnswer(answer)
	r := Result{Status: "unknown", Answer: answer, Reason: extracted.Reason, AnswerFormat: extracted.Format, FormatCompliant: extracted.Compliant}
	if extracted.Value != "" {
		r.NormalizedAnswer = &extracted.Value
		r.Status, r.Reason = "degraded", "wrong_answer"
		if extracted.Value == "21" {
			r.Status, r.Reason = "smart", "correct_answer"
		}
	}
	return r
}

func Payload(chat bool, profiles ...domain.IQProfile) map[string]any {
	profile := (domain.IQProfile{}).Defaults()
	if len(profiles) != 0 {
		profile = profiles[0].Defaults()
	}
	p := map[string]any{"model": profile.Model, "store": false, "stream": true}
	if chat {
		p["messages"] = []map[string]string{{"role": "system", "content": OutputInstruction}, {"role": "user", "content": Prompt}}
		if profile.ReasoningEffort != "upstream_default" {
			p["reasoning_effort"] = profile.ReasoningEffort
		}
	} else {
		p["instructions"] = OutputInstruction
		p["input"] = []map[string]any{{"role": "user", "content": []map[string]string{{"type": "input_text", "text": Prompt}}}}
		if profile.ReasoningEffort != "upstream_default" {
			p["reasoning"] = map[string]string{"effort": profile.ReasoningEffort}
		}
	}
	if profile.OutputMode == "strict" {
		schema := map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]string{"type": "integer"}}, "required": []string{"answer"}, "additionalProperties": false}
		format := map[string]any{"name": "candy_answer", "strict": true, "schema": schema}
		if chat {
			p["response_format"] = map[string]any{"type": "json_schema", "json_schema": format}
		} else {
			format["type"] = "json_schema"
			p["text"] = map[string]any{"format": format}
		}
	}
	return p
}
