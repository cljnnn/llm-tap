package recorder

import (
	"strings"
	"testing"
	"time"
)

func TestFormatSummaryIncludesToolCallsReasoningAndAssistantOutputs(t *testing.T) {
	record := RequestRecord{
		TraceID:     "trace_20260601_123456_789",
		StartedAt:   time.Date(2026, 6, 1, 12, 34, 56, 0, time.UTC),
		Method:      "POST",
		Path:        "/v1/chat/completions",
		UpstreamURL: "https://upstream.example.com/v1",
		StatusCode:  200,
		Duration:    123 * time.Millisecond,
		Stream:      false,
		Body: []byte(`{
			"model": "gpt-4.1-mini",
			"messages": [
				{
					"role": "assistant",
					"content": null,
					"tool_calls": [
						{
							"id": "call_1",
							"type": "function",
							"function": {
								"name": "lookup_weather",
								"arguments": "{\"city\":\"Beijing\"}"
							}
						}
					]
				},
				{
					"role": "tool",
					"tool_call_id": "call_1",
					"content": "sunny"
				}
			]
		}`),
		ResponseBody: []byte(`{
			"usage": {
				"prompt_tokens": 1234,
				"completion_tokens": 30,
				"total_tokens": 1234567,
				"prompt_tokens_details": {
					"cached_tokens": 80
				},
				"completion_tokens_details": {
					"reasoning_tokens": 12
				}
			},
			"choices": [
				{
					"message": {
						"content": "Final answer",
						"reasoning_content": "I used the tool result to answer."
					}
				}
			]
		}`),
	}

	summary := formatSummary(record)

	checks := []string{
		"#### Tool Calls",
		"`call_1`",
		"#### Tool Results — `call_1`",
		"```text\nsunny\n```",
		"## Reasoning Output",
		"I used the tool result to answer.",
		"## Usage",
		"- Input Tokens: `1,234 (1.23K)`",
		"- Output Tokens: `30`",
		"- Total Tokens: `1,234,567 (1.23M)`",
		"- Cached Input Tokens: `80`",
		"- Reasoning Tokens: `12`",
		"## Assistant Output",
		"Final answer",
	}

	for _, want := range checks {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q\n\nsummary:\n%s", want, summary)
		}
	}
}

func TestFormatSummaryIncludesStreamUsage(t *testing.T) {
	record := RequestRecord{
		TraceID:     "trace_20260601_123456_789",
		StartedAt:   time.Date(2026, 6, 1, 12, 34, 56, 0, time.UTC),
		Method:      "POST",
		Path:        "/v1/chat/completions",
		UpstreamURL: "https://upstream.example.com/v1",
		StatusCode:  200,
		Duration:    123 * time.Millisecond,
		Stream:      true,
		Body: []byte(`{
			"model": "gpt-4.1-mini",
			"messages": [{"role": "user", "content": "hello"}]
		}`),
		ResponseBody: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15,\"input_tokens_details\":{\"cached_tokens\":7}}}\n\n" +
			"data: [DONE]\n\n"),
	}

	summary := formatSummary(record)

	checks := []string{
		"## Usage",
		"- Input Tokens: `10`",
		"- Output Tokens: `5`",
		"- Total Tokens: `15`",
		"- Cached Input Tokens: `7`",
		"## Assistant Output",
		"Hi",
	}

	for _, want := range checks {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q\n\nsummary:\n%s", want, summary)
		}
	}
}
