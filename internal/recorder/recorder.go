package recorder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"llm-tap/internal/config"
)

type Recorder struct {
	cfg config.LoggingConfig
}

type RequestRecord struct {
	TraceID         string
	StartedAt       time.Time
	Method          string
	Path            string
	UpstreamURL     string
	Headers         map[string][]string
	Body            []byte
	ForwardBody     []byte
	StatusCode      int
	ResponseHeaders map[string][]string
	ResponseBody    []byte
	Duration        time.Duration
	Stream          bool
	Error           string
}

func New(cfg config.LoggingConfig) *Recorder {
	return &Recorder{cfg: cfg}
}

func NewTraceID(now time.Time) string {
	return fmt.Sprintf("trace_%s_%d", now.Format("20060102_150405"), now.UnixNano()%1_000_000)
}

func (r *Recorder) Record(record RequestRecord) error {
	dir := filepath.Join(r.cfg.Dir, record.StartedAt.Format("2006-01-02"), record.TraceID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	if err := writeJSON(filepath.Join(dir, "request.json"), requestLog(record), r.cfg.PrettyJSON); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "response.json"), responseLog(record), r.cfg.PrettyJSON); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(formatSummary(record)), 0o644); err != nil {
		return err
	}

	return nil
}

func writeJSON(path string, value any, pretty bool) error {
	var data []byte
	var err error
	if pretty {
		data, err = json.MarshalIndent(value, "", "  ")
	} else {
		data, err = json.Marshal(value)
	}
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func requestLog(record RequestRecord) map[string]any {
	log := map[string]any{
		"trace_id":     record.TraceID,
		"started_at":   record.StartedAt.Format(time.RFC3339Nano),
		"method":       record.Method,
		"path":         record.Path,
		"upstream_url": record.UpstreamURL,
		"headers":      record.Headers,
		"body":         decodeJSONOrString(record.Body),
	}

	if !bytes.Equal(record.Body, record.ForwardBody) {
		log["forward_body"] = decodeJSONOrString(record.ForwardBody)
	}

	return log
}

func responseLog(record RequestRecord) map[string]any {
	return map[string]any{
		"trace_id":    record.TraceID,
		"status_code": record.StatusCode,
		"duration_ms": record.Duration.Milliseconds(),
		"stream":      record.Stream,
		"headers":     record.ResponseHeaders,
		"body":        decodeResponseBody(record),
		"error":       record.Error,
	}
}

func decodeJSONOrString(data []byte) any {
	if len(bytes.TrimSpace(data)) == 0 {
		return ""
	}

	var value any
	if err := json.Unmarshal(data, &value); err == nil {
		return value
	}
	return string(data)
}

func decodeResponseBody(record RequestRecord) any {
	if record.Stream {
		return map[string]any{
			"raw":        string(record.ResponseBody),
			"text":       extractStreamText(record.ResponseBody),
			"reasoning":  extractStreamReasoning(record.ResponseBody),
			"tool_calls": extractStreamToolCalls(record.ResponseBody),
		}
	}
	return decodeJSONOrString(record.ResponseBody)
}

func formatSummary(record RequestRecord) string {
	var builder strings.Builder
	builder.WriteString("# LLM Trace\n\n")
	builder.WriteString(fmt.Sprintf("- Trace ID: `%s`\n", record.TraceID))
	builder.WriteString(fmt.Sprintf("- Started At: `%s`\n", record.StartedAt.Format(time.RFC3339)))
	builder.WriteString(fmt.Sprintf("- Method: `%s`\n", record.Method))
	builder.WriteString(fmt.Sprintf("- Path: `%s`\n", record.Path))
	builder.WriteString(fmt.Sprintf("- Upstream: `%s`\n", record.UpstreamURL))
	builder.WriteString(fmt.Sprintf("- Status: `%d`\n", record.StatusCode))
	builder.WriteString(fmt.Sprintf("- Latency: `%dms`\n", record.Duration.Milliseconds()))
	builder.WriteString(fmt.Sprintf("- Stream: `%t`\n", record.Stream))
	if record.Error != "" {
		builder.WriteString(fmt.Sprintf("- Error: `%s`\n", record.Error))
	}

	request := parseOpenAIRequest(record.Body)
	builder.WriteString(fmt.Sprintf("- Model: `%s`\n", emptyAsUnknown(request.Model)))
	builder.WriteString("\n")

	if len(request.Messages) > 0 {
		builder.WriteString("## Messages\n\n")
		for _, message := range request.Messages {
			builder.WriteString(formatMessageHeading(message))
			builder.WriteString(formatContent(message.Content))
			if len(message.ToolCalls) > 0 {
				if message.Content != nil && formatContent(message.Content) != "" {
					builder.WriteString("\n\n")
				}
				builder.WriteString("#### Tool Calls\n\n")
				builder.WriteString(formatToolCalls(message.ToolCalls))
			}
			builder.WriteString("\n\n")
		}
	}

	responseToolCalls := toolCallOutput(record)
	if responseToolCalls != "" {
		builder.WriteString("## Tool Calls\n\n")
		builder.WriteString(responseToolCalls)
		builder.WriteString("\n\n")
	}

	reasoningOutput := reasoningOutput(record)
	if reasoningOutput != "" {
		builder.WriteString("## Reasoning Output\n\n")
		builder.WriteString(reasoningOutput)
		builder.WriteString("\n\n")
	}

	assistantOutput := assistantOutput(record)
	if assistantOutput != "" {
		builder.WriteString("## Assistant Output\n\n")
		builder.WriteString(assistantOutput)
		builder.WriteString("\n")
	}

	return builder.String()
}

type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
}

type openAIMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCallID string     `json:"tool_call_id"`
	ToolCalls  []toolCall `json:"tool_calls"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func parseOpenAIRequest(body []byte) openAIRequest {
	var request openAIRequest
	_ = json.Unmarshal(body, &request)
	return request
}

func formatMessageHeading(message openAIMessage) string {
	if message.Role == "tool" {
		if message.ToolCallID != "" {
			return fmt.Sprintf("#### Tool Results — `%s`\n\n", message.ToolCallID)
		}
		return "#### Tool Results\n\n"
	}
	return fmt.Sprintf("### %s\n\n", emptyAsUnknown(message.Role))
}

func formatContent(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case nil:
		return ""
	default:
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", value)
		}
		return "```json\n" + string(data) + "\n```"
	}
}

func assistantOutput(record RequestRecord) string {
	if record.Stream {
		return extractStreamText(record.ResponseBody)
	}

	var response struct {
		Choices []struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(record.ResponseBody, &response); err != nil {
		return ""
	}

	var parts []string
	for _, choice := range response.Choices {
		if formatted := formatContent(choice.Message.Content); formatted != "" {
			parts = append(parts, formatted)
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}

func toolCallOutput(record RequestRecord) string {
	if record.Stream {
		return formatAnyToolCallDeltas(extractStreamToolCalls(record.ResponseBody))
	}

	var response struct {
		Choices []struct {
			Message struct {
				ToolCalls []toolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(record.ResponseBody, &response); err != nil {
		return ""
	}

	var parts []string
	for _, choice := range response.Choices {
		if len(choice.Message.ToolCalls) > 0 {
			parts = append(parts, formatToolCalls(choice.Message.ToolCalls))
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}

func reasoningOutput(record RequestRecord) string {
	if record.Stream {
		return extractStreamReasoning(record.ResponseBody)
	}

	var response struct {
		Choices []struct {
			Message map[string]any `json:"message"`
		} `json:"choices"`
	}

	if err := json.Unmarshal(record.ResponseBody, &response); err != nil {
		return ""
	}

	var parts []string
	for _, choice := range response.Choices {
		if formatted := formatFirstExistingField(choice.Message, reasoningFieldNames); formatted != "" {
			parts = append(parts, formatted)
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}

func extractStreamText(body []byte) string {
	var builder strings.Builder
	for _, event := range streamDataEvents(body) {
		var payload struct {
			Choices []struct {
				Delta struct {
					Content any `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(event), &payload); err != nil {
			continue
		}
		for _, choice := range payload.Choices {
			switch content := choice.Delta.Content.(type) {
			case string:
				builder.WriteString(content)
			}
		}
	}
	return builder.String()
}

func extractStreamReasoning(body []byte) string {
	var builder strings.Builder
	for _, event := range streamDataEvents(body) {
		var payload struct {
			Choices []struct {
				Delta map[string]any `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(event), &payload); err != nil {
			continue
		}
		for _, choice := range payload.Choices {
			if formatted := formatFirstExistingField(choice.Delta, reasoningFieldNames); formatted != "" {
				builder.WriteString(formatted)
			}
		}
	}
	return builder.String()
}

func extractStreamToolCalls(body []byte) []any {
	var calls []any
	for _, event := range streamDataEvents(body) {
		var payload struct {
			Choices []struct {
				Delta struct {
					ToolCalls any `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(event), &payload); err != nil {
			continue
		}
		for _, choice := range payload.Choices {
			if choice.Delta.ToolCalls != nil {
				calls = append(calls, choice.Delta.ToolCalls)
			}
		}
	}
	return calls
}

func formatToolCalls(toolCalls []toolCall) string {
	var builder strings.Builder
	for index, toolCall := range toolCalls {
		builder.WriteString(fmt.Sprintf("%d. `%s`", index+1, emptyAsUnknown(toolCall.Function.Name)))
		if toolCall.ID != "" {
			builder.WriteString(fmt.Sprintf(" — `%s`", toolCall.ID))
		}
		builder.WriteString("\n\n")

		arguments := strings.TrimSpace(toolCall.Function.Arguments)
		if arguments == "" {
			arguments = "{}"
		}
		builder.WriteString(formatJSONCodeBlock(arguments))
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}

func formatAnyToolCallDeltas(toolCallDeltas []any) string {
	if len(toolCallDeltas) == 0 {
		return ""
	}
	data, err := json.MarshalIndent(toolCallDeltas, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", toolCallDeltas)
	}
	return "```json\n" + string(data) + "\n```"
}

func formatJSONCodeBlock(value string) string {
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err == nil {
		data, err := json.MarshalIndent(decoded, "", "  ")
		if err == nil {
			return "```json\n" + string(data) + "\n```"
		}
	}
	return "```text\n" + value + "\n```"
}

func streamDataEvents(body []byte) []string {
	lines := strings.Split(string(body), "\n")
	events := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		events = append(events, data)
	}
	return events
}

func emptyAsUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

var reasoningFieldNames = []string{
	"reasoning_content",
	"reasoning",
	"thinking",
	"thought",
	"reasoning_details",
	"redacted_reasoning",
}

func formatFirstExistingField(payload map[string]any, fieldNames []string) string {
	for _, fieldName := range fieldNames {
		value, ok := payload[fieldName]
		if !ok {
			continue
		}
		if formatted := formatContent(value); formatted != "" {
			return formatted
		}
	}
	return ""
}
