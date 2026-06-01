package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"llm-tap/internal/config"
	"llm-tap/internal/recorder"
)

type Handler struct {
	cfg      config.Config
	recorder *recorder.Recorder
	client   *http.Client
}

func NewHandler(cfg config.Config, rec *recorder.Recorder) http.Handler {
	return &Handler{
		cfg:      cfg,
		recorder: rec,
		client: &http.Client{
			Timeout: cfg.Upstream.Timeout,
		},
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	traceID := recorder.NewTraceID(startedAt)

	requestBody, err := io.ReadAll(r.Body)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "read request body", err)
		return
	}
	defer r.Body.Close()

	upstreamURL, err := h.upstreamURL(r.URL)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "build upstream url", err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.Upstream.Timeout)
	defer cancel()

	upstreamRequest, err := http.NewRequestWithContext(ctx, r.Method, upstreamURL, bytes.NewReader(requestBody))
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "create upstream request", err)
		return
	}
	copyHeaders(upstreamRequest.Header, r.Header)
	upstreamRequest.Host = ""

	logRecord := recorder.RequestRecord{
		TraceID:     traceID,
		StartedAt:   startedAt,
		Method:      r.Method,
		Path:        r.URL.RequestURI(),
		UpstreamURL: upstreamURL,
		Headers:     sanitizeHeaders(r.Header),
		Body:        requestBody,
		ForwardBody: requestBody,
	}

	response, err := h.client.Do(upstreamRequest)
	if err != nil {
		logRecord.Error = err.Error()
		logRecord.Duration = time.Since(startedAt)
		if recordErr := h.recorder.Record(logRecord); recordErr != nil {
			log.Printf("record failed: %v", recordErr)
		}
		logTraceSummary(logRecord, h.recorder.SummaryPath(logRecord))
		h.writeError(w, http.StatusBadGateway, "forward request", err)
		return
	}
	defer response.Body.Close()

	copyHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)

	responseBody, err := h.copyResponse(w, response)
	if err != nil {
		log.Printf("copy response failed: trace_id=%s error=%v", traceID, err)
	}

	logRecord.StatusCode = response.StatusCode
	logRecord.ResponseHeaders = sanitizeHeaders(response.Header)
	logRecord.ResponseBody = responseBody
	logRecord.Duration = time.Since(startedAt)
	logRecord.Stream = isEventStream(response.Header.Get("Content-Type")) || requestWantsStream(requestBody)

	if recordErr := h.recorder.Record(logRecord); recordErr != nil {
		log.Printf("record failed: %v", recordErr)
	}
	logTraceSummary(logRecord, h.recorder.SummaryPath(logRecord))
}

func (h *Handler) upstreamURL(requestURL *url.URL) (string, error) {
	base, err := url.Parse(h.cfg.Upstream.BaseURL)
	if err != nil {
		return "", err
	}

	target := *base
	target.Path = joinURLPath(base.Path, requestURL.Path)
	target.RawQuery = requestURL.RawQuery
	return target.String(), nil
}

func (h *Handler) copyResponse(w http.ResponseWriter, response *http.Response) ([]byte, error) {
	var buffer bytes.Buffer
	writer := io.MultiWriter(&flushWriter{writer: w}, &buffer)
	_, err := io.CopyBuffer(writer, response.Body, make([]byte, 32*1024))
	return buffer.Bytes(), err
}

type flushWriter struct {
	writer io.Writer
}

func (w *flushWriter) Write(data []byte) (int, error) {
	written, err := w.writer.Write(data)
	if flusher, ok := w.writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return written, err
}

func (h *Handler) writeError(w http.ResponseWriter, status int, action string, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": fmt.Sprintf("%s: %v", action, err),
	})
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		if strings.EqualFold(key, "Host") || strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func sanitizeHeaders(headers http.Header) map[string][]string {
	sanitized := make(map[string][]string, len(headers))
	for key, values := range headers {
		if isSensitiveHeader(key) {
			sanitized[key] = []string{"[redacted]"}
			continue
		}
		sanitized[key] = append([]string(nil), values...)
	}
	return sanitized
}

func isSensitiveHeader(key string) bool {
	switch strings.ToLower(key) {
	case "authorization", "proxy-authorization", "x-api-key", "api-key", "cookie", "set-cookie":
		return true
	default:
		return false
	}
}

func joinURLPath(basePath, requestPath string) string {
	basePath = strings.TrimRight(basePath, "/")
	if requestPath == "" {
		return basePath
	}
	return basePath + requestPath
}

func isEventStream(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

func requestWantsStream(body []byte) bool {
	var payload struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return payload.Stream
}

func logTraceSummary(record recorder.RequestRecord, summaryPath string) {
	log.Printf(
		"trace_id=%s status=%d latency=%dms model=%s stream=%t path=%s upstream_url=%s summary=%s",
		record.TraceID,
		record.StatusCode,
		record.Duration.Milliseconds(),
		requestModel(record.Body),
		record.Stream,
		record.Path,
		record.UpstreamURL,
		summaryPath,
	)
}

func requestModel(body []byte) string {
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Model == "" {
		return "unknown"
	}
	return payload.Model
}
