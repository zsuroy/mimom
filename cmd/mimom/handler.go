package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

type ProxyHandler struct {
	cfg    *Config
	client *http.Client
	reason *ReasoningStore
	stats  *Stats
}

func NewProxyHandler(cfg *Config) *ProxyHandler {
	timeout := time.Duration(cfg.Server.Timeout) * time.Second
	return &ProxyHandler{
		cfg:    cfg,
		client: &http.Client{Timeout: timeout},
		reason: NewReasoningStore(),
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: 200}

	h.handle(rec, r)

	duration := time.Since(start)
	log.Printf("%s %s %s %d %s",
		r.Method,
		r.URL.Path,
		r.URL.RawQuery,
		rec.status,
		duration.Round(time.Millisecond),
	)

	if h.stats != nil {
		h.stats.RecordRequest(r.Method, r.URL.Path, rec.status, duration, false)
	}
}

func (h *ProxyHandler) handle(w *statusRecorder, r *http.Request) {
	// 鉴权
	if h.cfg.Server.APIKey != "" {
		auth := r.Header.Get("Authorization")
		if strings.TrimPrefix(auth, "Bearer ") != h.cfg.Server.APIKey {
			http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
			return
		}
	}

	var body []byte
	if r.Body != nil {
		var err error
		body, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, `{"error":{"message":"failed to read body"}}`, http.StatusBadRequest)
			return
		}
		r.Body.Close()
	}

	// 提取 model 和 stream 做路由
	var backend *BackendDef
	var realModel string
	isStream := false

	if len(body) > 0 {
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if json.Unmarshal(body, &req) == nil && req.Model != "" {
			backend, realModel, _ = h.cfg.LookupModel(req.Model)
			isStream = req.Stream
		}
	}

	if backend == nil {
		backend = h.cfg.DefaultBackend()
	}
	if backend == nil {
		http.Error(w, `{"error":{"message":"no backend available"}}`, http.StatusBadGateway)
		return
	}

	// ★ 注入缓存的 reasoning/thinking
	if len(body) > 0 {
		if patched, err := h.reason.InjectReasoning(body); err == nil {
			body = patched
		}
	}

	streamCacheKey := ""
	if len(body) > 0 {
		streamCacheKey = h.lastAssistantHash(body)
	}

	// 构建后端 URL：base_url（完整前缀）+ 请求路径中 /v1 之后的部分
	// 例：base_url="https://api.xiaomimimo.com/v1" + "/chat/completions"
	//     base_url="https://api.anthropic.com/v1" + "/messages"
	//     base_url="https://xxx.xiaomimimo.com/anthropic" + "/messages"
	suffix := strings.TrimPrefix(r.URL.Path, "/v1")
	targetURL := strings.TrimRight(backend.BaseURL, "/") + suffix
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	// 替换 model 字段
	var reqBody io.Reader
	if realModel != "" && len(body) > 0 {
		patched, err := replaceModelField(body, realModel)
		if err != nil {
			http.Error(w, `{"error":{"message":"failed to patch model field"}}`, http.StatusInternalServerError)
			return
		}
		reqBody = bytes.NewReader(patched)
	} else if len(body) > 0 {
		reqBody = bytes.NewReader(body)
	}

	backendReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, reqBody)
	if err != nil {
		http.Error(w, `{"error":{"message":"failed to create request"}}`, http.StatusInternalServerError)
		return
	}

	// 复制原始 header（跳过鉴权和 Content-Length）
	for k, vv := range r.Header {
		if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vv {
			backendReq.Header.Add(k, v)
		}
	}

	// ★ 按后端类型设置鉴权 header
	if backend.IsAnthropic() {
		backendReq.Header.Set("x-api-key", backend.APIKey)
		backendReq.Header.Set("anthropic-version", "2023-06-01")
	} else {
		backendReq.Header.Set("Authorization", "Bearer "+backend.APIKey)
	}

	resp, err := h.client.Do(backendReq)
	if err != nil {
		w.status = http.StatusBadGateway
		http.Error(w, `{"error":{"message":"backend request failed"}}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		w.status = resp.StatusCode
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	if isStream {
		h.handleStreaming(w, resp, streamCacheKey, backend.IsAnthropic())
	} else {
		h.handleNonStreaming(w, resp, backend.IsAnthropic())
	}
}

func (h *ProxyHandler) handleNonStreaming(w *statusRecorder, resp *http.Response, isAnthropic bool) {
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		w.status = http.StatusBadGateway
		http.Error(w, `{"error":{"message":"failed to read response"}}`, http.StatusBadGateway)
		return
	}

	if isAnthropic {
		go h.reason.ExtractAnthropicThinking(respBody)
	} else {
		go h.reason.ExtractReasoning(respBody)
	}

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

func (h *ProxyHandler) handleStreaming(w *statusRecorder, resp *http.Response, cacheKey string, isAnthropic bool) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.ResponseWriter.(http.Flusher)
	if !ok {
		io.Copy(w, resp.Body)
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	var thinkingChunks []string
	var currentEvent string // Anthropic: 记录当前 event 类型

	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "%s\n", line)
		if line == "" {
			flusher.Flush()
		}

		if isAnthropic {
			// Anthropic SSE: "event: xxx" 行标识事件类型
			if strings.HasPrefix(line, "event: ") {
				currentEvent = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") && currentEvent == "content_block_delta" {
				data := strings.TrimPrefix(line, "data: ")
				h.collectAnthropicStreamThinking(data, &thinkingChunks)
			}
		} else {
			// OpenAI SSE
			if strings.HasPrefix(line, "data: ") && !strings.HasSuffix(line, "[DONE]") {
				data := strings.TrimPrefix(line, "data: ")
				h.collectStreamReasoning(data, &thinkingChunks)
			}
		}
	}
	flusher.Flush()

	if len(thinkingChunks) > 0 && cacheKey != "" {
		h.reason.CacheStreamReasoning(cacheKey, strings.Join(thinkingChunks, ""))
	}

	if err := scanner.Err(); err != nil {
		log.Printf("stream error: %v", err)
	}
}

func (h *ProxyHandler) lastAssistantHash(body []byte) string {
	var raw struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return ""
	}
	for i := len(raw.Messages) - 1; i >= 0; i-- {
		if string(raw.Messages[i]["role"]) == `"assistant"` {
			return HashAssistantMsg(raw.Messages[i])
		}
	}
	return ""
}

func (h *ProxyHandler) collectStreamReasoning(data string, chunks *[]string) {
	var chunk struct {
		Choices []struct {
			Delta struct {
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(data), &chunk) != nil {
		return
	}
	for _, c := range chunk.Choices {
		if c.Delta.ReasoningContent != "" {
			*chunks = append(*chunks, c.Delta.ReasoningContent)
		}
	}
}

// collectAnthropicStreamThinking 从 Anthropic SSE chunk 中提取 thinking delta。
func (h *ProxyHandler) collectAnthropicStreamThinking(data string, chunks *[]string) {
	var event struct {
		Type         string `json:"type"`
		ContentBlock struct {
			Type     string `json:"type"`
			Thinking string `json:"thinking"`
		} `json:"content_block"`
		Delta struct {
			Type     string `json:"type"`
			Thinking string `json:"thinking"`
		} `json:"delta"`
	}
	if json.Unmarshal([]byte(data), &event) != nil {
		return
	}
	// content_block_start 中的初始 thinking
	if event.ContentBlock.Type == "thinking" && event.ContentBlock.Thinking != "" {
		*chunks = append(*chunks, event.ContentBlock.Thinking)
	}
	// content_block_delta 中的增量 thinking
	if event.Delta.Type == "thinking_delta" && event.Delta.Thinking != "" {
		*chunks = append(*chunks, event.Delta.Thinking)
	}
}

func replaceModelField(body []byte, newModel string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	modelBytes, err := json.Marshal(newModel)
	if err != nil {
		return nil, err
	}
	m["model"] = modelBytes
	return json.Marshal(m)
}
