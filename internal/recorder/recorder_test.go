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
		"## Reasoning Output",
		"I used the tool result to answer.",
		"## Assistant Output",
		"Final answer",
	}

	for _, want := range checks {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q\n\nsummary:\n%s", want, summary)
		}
	}
}
