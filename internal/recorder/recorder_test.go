package recorder

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"llm-tap/internal/config"
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
			"temperature": 0.2,
			"max_tokens": 1024,
			"stream": false,
			"metadata": {"feature": "debug"},
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
			"id": "chatcmpl_123",
			"object": "chat.completion",
			"created": 1780317296,
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
		"## Request Parameters",
		"- max_tokens: `1024`",
		"- metadata:\n```json\n{\n  \"feature\": \"debug\"\n}\n```",
		"- stream: `false`",
		"- temperature: `0.2`",
		"## Response Metadata",
		"- created: `1780317296`",
		"- id: `chatcmpl_123`",
		"- object: `chat.completion`",
		"## Reasoning Output",
		"I used the tool result to answer.",
		"## Usage",
		"- Input Tokens: `1,234 (1.23K)`",
		"- Output Tokens: `30`",
		"- Total Tokens: `1,234,567 (1.23M)`",
		"- Cached Input Tokens: `80`",
		"- Cache Hit Rate: `6.5%`",
		"- Reasoning Tokens: `12`",
		"## Assistant Output",
		"Final answer",
	}

	for _, want := range checks {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q\n\nsummary:\n%s", want, summary)
		}
	}

	assertSectionOrder(t, summary, "## Artifacts", "## Usage", "## Request Parameters")
}

func TestFormatToolResultContentDecodesNestedJSONStringPayloads(t *testing.T) {
	rendered := formatMessageContent(openAIMessage{
		Role: "tool",
		Content: `{
			"nested_object": "{\"foo\":\"bar\"}",
			"nested_array": "[{\"id\":1},{\"id\":2}]",
			"status": "ok"
		}`,
	})

	for _, want := range []string{
		"```json",
		"\"nested_object\": {",
		"\"foo\": \"bar\"",
		"\"nested_array\": [",
		"\"id\": 1",
		"\"status\": \"ok\"",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered tool result missing %q\n\nrendered:\n%s", want, rendered)
		}
	}

	for _, unwanted := range []string{`"{\\\"foo\\\":\\\"bar\\\"}"`, `"[{\\\"id\\\":1},{\\\"id\\\":2}]"`} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("rendered tool result should decode nested JSON strings, found %q\n\nrendered:\n%s", unwanted, rendered)
		}
	}
}

func TestFormatToolResultContentRendersPlainTextAsTextCodeBlock(t *testing.T) {
	rendered := formatMessageContent(openAIMessage{
		Role:    "tool",
		Content: "just a plain tool result",
	})

	if rendered != "```text\njust a plain tool result\n```" {
		t.Fatalf("rendered tool result = %q, want plain text code block", rendered)
	}
}

func TestFormatSummaryCanPreserveNestedJSONStringPayloads(t *testing.T) {
	record := RequestRecord{
		TraceID:     "trace_20260601_123456_789",
		StartedAt:   time.Date(2026, 6, 1, 12, 34, 56, 0, time.UTC),
		Method:      "POST",
		Path:        "/v1/chat/completions",
		UpstreamURL: "https://upstream.example.com/v1",
		StatusCode:  200,
		Duration:    123 * time.Millisecond,
		Body: []byte(`{
			"model": "gpt-4.1-mini",
			"messages": [
				{
					"role": "tool",
					"content": "{\"nested_object\":\"{\\\"foo\\\":\\\"bar\\\"}\",\"nested_array\":\"[{\\\"id\\\":1}]\"}"
				}
			]
		}`),
	}

	summary := formatSummaryWithOptions(record, summaryFormatOptions{ExpandNestedJSON: false})

	for _, want := range []string{`"nested_object": "{\"foo\":\"bar\"}"`, `"nested_array": "[{\"id\":1}]"`} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary should preserve nested JSON string %q\n\nsummary:\n%s", want, summary)
		}
	}

	for _, unwanted := range []string{"\"nested_object\": {", "\"foo\": \"bar\"", "\"nested_array\": ["} {
		if strings.Contains(summary, unwanted) {
			t.Fatalf("summary should not expand nested JSON string %q\n\nsummary:\n%s", unwanted, summary)
		}
	}
}

func TestFormatSummaryIncludesArtifactsProviderDiagnosticsFinishReasonsAndCacheEfficiency(t *testing.T) {
	record := RequestRecord{
		TraceID:     "trace_20260601_123456_789",
		StartedAt:   time.Date(2026, 6, 1, 12, 34, 56, 0, time.UTC),
		Method:      "POST",
		Path:        "/v1/chat/completions",
		UpstreamURL: "https://upstream.example.com/v1",
		StatusCode:  200,
		Duration:    123 * time.Millisecond,
		Stream:      false,
		ResponseHeaders: map[string][]string{
			"OpenAI-Request-ID":              {"req_123"},
			"x-ratelimit-remaining-requests": {"9"},
			"X-Request-ID":                   {"abc", "def"},
		},
		Body: []byte(`{
			"model": "gpt-4.1-mini",
			"messages": [{"role": "user", "content": "hello"}]
		}`),
		ResponseBody: []byte(`{
			"usage": {
				"prompt_tokens": 100,
				"completion_tokens": 20,
				"total_tokens": 120,
				"prompt_tokens_details": {
					"cached_tokens": 25
				}
			},
			"choices": [
				{
					"finish_reason": "stop",
					"message": {"content": "done"}
				},
				{
					"finish_reason": "tool_calls",
					"message": {"content": "more"}
				}
			]
		}`),
	}

	summary := formatSummary(record)

	checks := []string{
		"## Artifacts",
		"- Request JSON: `request.json`",
		"- Response JSON: `response.json`",
		"## Provider Diagnostics",
		"- openai-request-id: `req_123`",
		"- x-request-id: `abc, def`",
		"## Finish Reasons",
		"- Choice 1: `stop`",
		"- Choice 2: `tool_calls`",
		"## Usage",
		"- Cached Input Tokens: `25`",
		"- Cache Hit Rate: `25.0%`",
	}

	for _, want := range checks {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q\n\nsummary:\n%s", want, summary)
		}
	}

	if strings.Contains(summary, "## Cache Efficiency") {
		t.Fatalf("summary should not include standalone Cache Efficiency section\n\nsummary:\n%s", summary)
	}
}

func assertSectionOrder(t *testing.T, content string, sections ...string) {
	t.Helper()

	previousIndex := -1
	for _, section := range sections {
		index := strings.Index(content, section)
		if index == -1 {
			t.Fatalf("summary missing section %q\n\nsummary:\n%s", section, content)
		}
		if index <= previousIndex {
			t.Fatalf("summary section %q is out of order\n\nsummary:\n%s", section, content)
		}
		previousIndex = index
	}
}

func TestRecorderSummaryPath(t *testing.T) {
	recorder := New(config.LoggingConfig{Dir: "logs"})
	record := RequestRecord{
		TraceID:   "trace_20260601_123456_789",
		StartedAt: time.Date(2026, 6, 1, 12, 34, 56, 0, time.UTC),
	}

	want := filepath.Join("logs", "2026-06-01", "trace_20260601_123456_789", "summary.md")
	if got := recorder.SummaryPath(record); got != want {
		t.Fatalf("summary path = %q, want %q", got, want)
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

func TestFormatSummaryIncludesStreamFinishReasons(t *testing.T) {
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
			"data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n" +
			"data: {\"choices\":[{\"finish_reason\":\"length\"}]}\n\n" +
			"data: [DONE]\n\n"),
	}

	summary := formatSummary(record)

	checks := []string{
		"## Finish Reasons",
		"- Choice 1: `stop`",
		"- Choice 2: `length`",
	}

	for _, want := range checks {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q\n\nsummary:\n%s", want, summary)
		}
	}
}
