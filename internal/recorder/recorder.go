package recorder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

func (r *Recorder) SummaryPath(record RequestRecord) string {
	return filepath.Join(r.traceDir(record), "summary.md")
}

func (r *Recorder) Record(record RequestRecord) error {
	dir := r.traceDir(record)
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

func (r *Recorder) traceDir(record RequestRecord) string {
	return filepath.Join(r.cfg.Dir, record.StartedAt.Format("2006-01-02"), record.TraceID)
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

	if artifacts := artifactsOutput(); artifacts != "" {
		builder.WriteString("## Artifacts\n\n")
		builder.WriteString(artifacts)
		builder.WriteString("\n\n")
	}

	if usage := usageOutput(record); usage != "" {
		builder.WriteString("## Usage\n\n")
		builder.WriteString(usage)
		builder.WriteString("\n\n")
	}

	if requestParameters := requestParametersOutput(record.Body); requestParameters != "" {
		builder.WriteString("## Request Parameters\n\n")
		builder.WriteString(requestParameters)
		builder.WriteString("\n\n")
	}

	if providerDiagnostics := providerDiagnosticsOutput(record.ResponseHeaders); providerDiagnostics != "" {
		builder.WriteString("## Provider Diagnostics\n\n")
		builder.WriteString(providerDiagnostics)
		builder.WriteString("\n\n")
	}

	if responseMetadata := responseMetadataOutput(record); responseMetadata != "" {
		builder.WriteString("## Response Metadata\n\n")
		builder.WriteString(responseMetadata)
		builder.WriteString("\n\n")
	}

	if finishReasons := finishReasonsOutput(record); finishReasons != "" {
		builder.WriteString("## Finish Reasons\n\n")
		builder.WriteString(finishReasons)
		builder.WriteString("\n\n")
	}

	if len(request.Messages) > 0 {
		builder.WriteString("## Messages\n\n")
		for _, message := range request.Messages {
			builder.WriteString(formatMessageHeading(message))
			builder.WriteString(formatMessageContent(message))
			if len(message.ToolCalls) > 0 {
				if message.Content != nil && formatMessageContent(message) != "" {
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

func artifactsOutput() string {
	return "- Request JSON: `request.json`\n- Response JSON: `response.json`"
}

func providerDiagnosticsOutput(headers map[string][]string) string {
	if len(headers) == 0 {
		return ""
	}

	targets := []string{
		"x-request-id",
		"request-id",
		"openai-request-id",
		"anthropic-request-id",
		"cf-ray",
		"openai-processing-ms",
		"x-ratelimit-limit-requests",
		"x-ratelimit-remaining-requests",
		"x-ratelimit-reset-requests",
		"x-ratelimit-limit-tokens",
		"x-ratelimit-remaining-tokens",
		"x-ratelimit-reset-tokens",
	}

	var lines []string
	for _, target := range targets {
		values, ok := lookupHeaderValues(headers, target)
		if !ok {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s: `%s`", target, strings.Join(values, ", ")))
	}

	return strings.Join(lines, "\n")
}

func lookupHeaderValues(headers map[string][]string, target string) ([]string, bool) {
	for key, values := range headers {
		if strings.EqualFold(key, target) {
			return values, true
		}
	}
	return nil, false
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

func formatMessageContent(message openAIMessage) string {
	if message.Role == "tool" {
		return formatToolResultContent(message.Content)
	}
	return formatContent(message.Content)
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

func formatToolResultContent(content any) string {
	switch value := content.(type) {
	case string:
		return formatJSONCodeBlock(strings.TrimSpace(value))
	case nil:
		return ""
	default:
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return fmt.Sprintf("```text\n%v\n```", value)
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

func finishReasonsOutput(record RequestRecord) string {
	var reasons []string
	if record.Stream {
		reasons = extractStreamFinishReasons(record.ResponseBody)
	} else {
		reasons = responseFinishReasons(record.ResponseBody)
	}

	if len(reasons) == 0 {
		return ""
	}

	var lines []string
	for index, reason := range reasons {
		lines = append(lines, fmt.Sprintf("- Choice %d: `%s`", index+1, reason))
	}
	return strings.Join(lines, "\n")
}

func responseFinishReasons(body []byte) []string {
	var response struct {
		Choices []struct {
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil
	}

	var reasons []string
	for _, choice := range response.Choices {
		if choice.FinishReason == nil || *choice.FinishReason == "" {
			continue
		}
		reasons = append(reasons, *choice.FinishReason)
	}
	return reasons
}

func extractStreamFinishReasons(body []byte) []string {
	var reasons []string
	for _, event := range streamDataEvents(body) {
		var payload struct {
			Choices []struct {
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(event), &payload); err != nil {
			continue
		}
		for _, choice := range payload.Choices {
			if choice.FinishReason == nil || *choice.FinishReason == "" {
				continue
			}
			reasons = append(reasons, *choice.FinishReason)
		}
	}
	return reasons
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

func requestParametersOutput(body []byte) string {
	parameters := topLevelJSONObject(body)
	delete(parameters, "model")
	delete(parameters, "messages")
	return formatObjectFields(parameters)
}

func responseMetadataOutput(record RequestRecord) string {
	metadata := responseMetadata(record)
	delete(metadata, "choices")
	delete(metadata, "usage")
	return formatObjectFields(metadata)
}

func responseMetadata(record RequestRecord) map[string]any {
	if record.Stream {
		return streamResponseMetadata(record.ResponseBody)
	}
	return topLevelJSONObject(record.ResponseBody)
}

func streamResponseMetadata(body []byte) map[string]any {
	metadata := map[string]any{}
	for _, event := range streamDataEvents(body) {
		payload := topLevelJSONObject([]byte(event))
		for key, value := range payload {
			if _, exists := metadata[key]; !exists {
				metadata[key] = value
			}
		}
	}
	return metadata
}

func topLevelJSONObject(data []byte) map[string]any {
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		return map[string]any{}
	}
	return object
}

func formatObjectFields(fields map[string]any) string {
	if len(fields) == 0 {
		return ""
	}

	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var builder strings.Builder
	for _, key := range keys {
		value := fields[key]
		if formatted, ok := formatScalarFieldValue(value); ok {
			builder.WriteString(fmt.Sprintf("- %s: `%s`\n", key, formatted))
			continue
		}

		builder.WriteString(fmt.Sprintf("- %s:\n", key))
		builder.WriteString(formatAnyAsJSONCodeBlock(value))
		builder.WriteString("\n")
	}

	return strings.TrimSpace(builder.String())
}

func formatScalarFieldValue(value any) (string, bool) {
	switch typed := value.(type) {
	case nil:
		return "null", true
	case string:
		if strings.ContainsAny(typed, "`\n") {
			return "", false
		}
		return typed, true
	case bool:
		return strconv.FormatBool(typed), true
	case float64:
		if math.Trunc(typed) == typed {
			return fmt.Sprintf("%.0f", typed), true
		}
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case json.Number:
		return typed.String(), true
	default:
		return "", false
	}
}

func formatAnyAsJSONCodeBlock(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("```text\n%v\n```", value)
	}
	return "```json\n" + string(data) + "\n```"
}

func usageOutput(record RequestRecord) string {
	usage := responseUsage(record)
	if len(usage) == 0 {
		return ""
	}

	fields := []struct {
		label string
		paths [][]string
	}{
		{label: "Input Tokens", paths: [][]string{{"prompt_tokens"}, {"input_tokens"}}},
		{label: "Output Tokens", paths: [][]string{{"completion_tokens"}, {"output_tokens"}}},
		{label: "Total Tokens", paths: [][]string{{"total_tokens"}}},
		{label: "Cached Input Tokens", paths: [][]string{{"prompt_tokens_details", "cached_tokens"}, {"input_tokens_details", "cached_tokens"}}},
		{label: "Cache Creation Input Tokens", paths: [][]string{{"cache_creation_input_tokens"}}},
		{label: "Cache Read Input Tokens", paths: [][]string{{"cache_read_input_tokens"}}},
		{label: "Reasoning Tokens", paths: [][]string{{"completion_tokens_details", "reasoning_tokens"}, {"output_tokens_details", "reasoning_tokens"}}},
	}

	var lines []string
	for _, field := range fields {
		if value, ok := firstUsageTokenCount(usage, field.paths); ok {
			lines = append(lines, fmt.Sprintf("- %s: `%s`", field.label, value))
		}
	}
	if hitRate, ok := cacheHitRate(usage); ok {
		lines = append(lines, fmt.Sprintf("- Cache Hit Rate: `%.1f%%`", hitRate))
	}

	return strings.Join(lines, "\n")
}

func cacheHitRate(usage map[string]any) (float64, bool) {
	input, ok := usageNumber(usage, []string{"prompt_tokens"})
	if !ok {
		input, ok = usageNumber(usage, []string{"input_tokens"})
	}
	if !ok || input <= 0 {
		return 0, false
	}

	cached, ok := usageNumber(usage, []string{"prompt_tokens_details", "cached_tokens"})
	if !ok {
		cached, ok = usageNumber(usage, []string{"input_tokens_details", "cached_tokens"})
	}
	if !ok {
		return 0, false
	}

	return (cached / input) * 100, true
}

func responseUsage(record RequestRecord) map[string]any {
	if record.Stream {
		return streamUsage(record.ResponseBody)
	}

	var response struct {
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(record.ResponseBody, &response); err != nil {
		return nil
	}
	return response.Usage
}

func streamUsage(body []byte) map[string]any {
	var usage map[string]any
	for _, event := range streamDataEvents(body) {
		var payload struct {
			Usage map[string]any `json:"usage"`
		}
		if err := json.Unmarshal([]byte(event), &payload); err != nil {
			continue
		}
		if len(payload.Usage) > 0 {
			usage = payload.Usage
		}
	}
	return usage
}

func firstUsageTokenCount(payload map[string]any, paths [][]string) (string, bool) {
	for _, path := range paths {
		if value, ok := usageNumber(payload, path); ok {
			return formatTokenCount(value), true
		}
	}
	return "", false
}

func usageNumber(payload map[string]any, path []string) (float64, bool) {
	var current any = payload
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return 0, false
		}
		current, ok = object[key]
		if !ok {
			return 0, false
		}
	}

	switch value := current.(type) {
	case float64:
		return value, true
	case json.Number:
		number, err := value.Float64()
		return number, err == nil
	case string:
		if strings.TrimSpace(value) == "" {
			return 0, false
		}
		number, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, false
		}
		return number, true
	default:
		return 0, false
	}
}

func formatTokenCount(value float64) string {
	rounded := int64(math.Round(value))
	exact := formatIntWithCommas(rounded)

	abs := math.Abs(float64(rounded))
	switch {
	case abs >= 1_000_000:
		return fmt.Sprintf("%s (%.2fM)", exact, float64(rounded)/1_000_000)
	case abs >= 1_000:
		return fmt.Sprintf("%s (%.2fK)", exact, float64(rounded)/1_000)
	default:
		return exact
	}
}

func formatIntWithCommas(value int64) string {
	negative := value < 0
	if negative {
		value = -value
	}

	digits := strconv.FormatInt(value, 10)
	firstGroupLength := len(digits) % 3
	if firstGroupLength == 0 {
		firstGroupLength = 3
	}

	var builder strings.Builder
	if negative {
		builder.WriteString("-")
	}
	builder.WriteString(digits[:firstGroupLength])
	for index := firstGroupLength; index < len(digits); index += 3 {
		builder.WriteString(",")
		builder.WriteString(digits[index : index+3])
	}
	return builder.String()
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
