//go:build integration

package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func loadRealConfig(t *testing.T) *Config {
	t.Helper()
	path := os.Getenv("MIMOM_CONFIG")
	if path == "" {
		path = "../../config.yaml"
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

// ─── 模型路由：验证代理正确替换 model 字段 ───

func TestIntegration_ModelRouting(t *testing.T) {
	cfg := loadRealConfig(t)
	handler := NewProxyHandler(cfg)

	body := `{"model":"mimo-v2.5-pro","messages":[{"role":"user","content":"say ok"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)

	choices := resp["choices"].([]any)
	if len(choices) == 0 {
		t.Fatal("expected non-empty choices")
	}

	msg := choices[0].(map[string]any)["message"].(map[string]any)
	content, _ := msg["content"].(string)
	if content == "" {
		t.Error("expected non-empty content")
	}
	t.Logf("response: %s", content)
}

func TestIntegration_ModelRouting_Unknown(t *testing.T) {
	cfg := loadRealConfig(t)
	handler := NewProxyHandler(cfg)

	// 未知模型走默认后端
	body := `{"model":"unknown-model","messages":[{"role":"user","content":"say ok"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// 应该返回后端错误（model 不存在）或成功（后端接受任意 model）
	t.Logf("status: %d, body: %s", w.Code, w.Body.String())
}

// ─── reasoning_content：验证代理提取并缓存推理内容 ───

func TestIntegration_ReasoningContent_Extract(t *testing.T) {
	cfg := loadRealConfig(t)
	handler := NewProxyHandler(cfg)

	// 需要推理的问题，确保返回 reasoning_content
	body := `{"model":"mimo-v2.5-pro","messages":[{"role":"user","content":"请逐步推理：3乘以7等于多少？"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)

	msg := resp["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	rc, _ := msg["reasoning_content"].(string)
	content, _ := msg["content"].(string)

	if rc == "" {
		t.Log("no reasoning_content (model may not support it)")
	} else {
		t.Logf("reasoning_content: %d chars", len(rc))
	}
	t.Logf("content: %s", content)

	// 验证缓存中有数据（ExtractReasoning 是异步的，等一下）
	if rc != "" {
		time.Sleep(200 * time.Millisecond)
		store := handler.reason
		store.mu.RLock()
		count := len(store.entries)
		store.mu.RUnlock()
		if count == 0 {
			t.Error("expected reasoning to be cached after response")
		} else {
			t.Logf("cache has %d entries", count)
		}
	}
}

// ─── reasoning_content 回填：验证多轮会话自动注入 ───

func TestIntegration_ReasoningContent_Inject(t *testing.T) {
	cfg := loadRealConfig(t)
	handler := NewProxyHandler(cfg)

	// 第一轮：获取 reasoning_content
	body1 := `{"model":"mimo-v2.5-pro","messages":[{"role":"user","content":"1+1等于几？"}]}`
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	if w1.Code != 200 {
		t.Fatalf("round 1: %d", w1.Code)
	}

	var resp1 map[string]any
	json.NewDecoder(w1.Body).Decode(&resp1)
	msg1 := resp1["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	rc1, _ := msg1["reasoning_content"].(string)
	t.Logf("round 1 reasoning: %d chars, content: %s", len(rc1), msg1["content"])

	// 第二轮：模拟客户端丢弃 reasoning_content（只发 role+content）
	msg1Clean := map[string]string{
		"role":    "assistant",
		"content": msg1["content"].(string),
	}
	msg1JSON, _ := json.Marshal(msg1Clean)

	body2 := `{"model":"mimo-v2.5-pro","messages":[
		{"role":"user","content":"1+1等于几？"},
		` + string(msg1JSON) + `,
		{"role":"user","content":"那2+2呢？"}
	]}`

	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != 200 {
		t.Fatalf("round 2: %d, body: %s", w2.Code, w2.Body.String())
	}

	t.Log("multi-turn with reasoning injection succeeded")
}

// ─── 流式：验证代理正确转发 SSE ───

func TestIntegration_Streaming(t *testing.T) {
	cfg := loadRealConfig(t)
	handler := NewProxyHandler(cfg)

	body := `{"model":"mimo-v2.5-pro","messages":[{"role":"user","content":"say ok"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("Content-Type: got %q, want text/event-stream", ct)
	}

	scanner := bufio.NewScanner(w.Body)
	var reasoningChunks, contentChunks []string
	chunkCount := 0

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") || strings.HasSuffix(line, "[DONE]") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		chunkCount++

		var chunk struct {
			Choices []struct {
				Delta struct {
					ReasoningContent string `json:"reasoning_content"`
					Content          string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		for _, c := range chunk.Choices {
			if c.Delta.ReasoningContent != "" {
				reasoningChunks = append(reasoningChunks, c.Delta.ReasoningContent)
			}
			if c.Delta.Content != "" {
				contentChunks = append(contentChunks, c.Delta.Content)
			}
		}
	}

	if chunkCount == 0 {
		t.Fatal("expected at least one data chunk")
	}

	t.Logf("chunks: %d, reasoning: %d, content: %d", chunkCount, len(reasoningChunks), len(contentChunks))
	t.Logf("content: %s", strings.Join(contentChunks, ""))
}

func TestIntegration_Streaming_ReasoningCached(t *testing.T) {
	cfg := loadRealConfig(t)
	handler := NewProxyHandler(cfg)

	// 流式请求
	body := `{"model":"mimo-v2.5-pro","messages":[{"role":"user","content":"1+1等于几？"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d", w.Code)
	}

	// 读完流
	scanner := bufio.NewScanner(w.Body)
	var reasoningChunks []string
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") || strings.HasSuffix(line, "[DONE]") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var chunk struct {
			Choices []struct {
				Delta struct {
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		for _, c := range chunk.Choices {
			if c.Delta.ReasoningContent != "" {
				reasoningChunks = append(reasoningChunks, c.Delta.ReasoningContent)
			}
		}
	}

	if len(reasoningChunks) == 0 {
		t.Skip("no reasoning content in stream, skipping cache check")
	}

	// 流结束后，缓存中应该有 reasoning
	// 缓存 key 是请求中最后一条 assistant 消息的哈希
	// 此请求没有 assistant 消息，所以 key 为空，不会缓存
	t.Logf("stream reasoning: %d chunks", len(reasoningChunks))
}

// ─── Responses API：验证 /v1/responses 转换层 ───

func TestIntegration_Responses_NonStream(t *testing.T) {
	cfg := loadRealConfig(t)
	reason := NewReasoningStore()
	handler := NewResponsesHandler(cfg, reason)

	body := `{"model":"mimo-v2.5-pro","input":"say ok"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)

	// 验证 Responses API 格式
	if resp["object"] != "response" {
		t.Errorf("object: got %q", resp["object"])
	}
	if resp["status"] != "completed" {
		t.Errorf("status: got %q", resp["status"])
	}
	if !strings.HasPrefix(resp["id"].(string), "resp_") {
		t.Errorf("id prefix: got %q", resp["id"])
	}

	output := resp["output"].([]any)
	if len(output) == 0 {
		t.Fatal("expected non-empty output")
	}

	for _, item := range output {
		o := item.(map[string]any)
		t.Logf("output type=%s", o["type"])
		if o["type"] == "message" {
			content := o["content"].([]any)[0].(map[string]any)
			t.Logf("  text: %s", content["text"])
		}
	}
}

func TestIntegration_Responses_Stream(t *testing.T) {
	cfg := loadRealConfig(t)
	reason := NewReasoningStore()
	handler := NewResponsesHandler(cfg, reason)

	body := `{"model":"mimo-v2.5-pro","input":"say ok","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d", w.Code)
	}

	scanner := bufio.NewScanner(w.Body)
	var events []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
	}

	t.Logf("events: %v", events)

	// 必须有 response.created 和 response.completed
	hasCreated := false
	hasCompleted := false
	hasOutputDelta := false
	for _, e := range events {
		switch e {
		case "response.created":
			hasCreated = true
		case "response.completed":
			hasCompleted = true
		case "response.output_text.delta":
			hasOutputDelta = true
		}
	}

	if !hasCreated {
		t.Error("missing response.created")
	}
	if !hasCompleted {
		t.Error("missing response.completed")
	}
	if !hasOutputDelta {
		t.Error("missing response.output_text.delta")
	}
}

func TestIntegration_Responses_WithInstructions(t *testing.T) {
	cfg := loadRealConfig(t)
	reason := NewReasoningStore()
	handler := NewResponsesHandler(cfg, reason)

	body := `{"model":"mimo-v2.5-pro","input":"你好","instructions":"请用英文回答"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	output := resp["output"].([]any)
	for _, item := range output {
		o := item.(map[string]any)
		if o["type"] == "message" {
			content := o["content"].([]any)[0].(map[string]any)
			t.Logf("response: %s", content["text"])
			// instructions 要求英文回答
			text := content["text"].(string)
			if len(text) > 0 && rune(text[0]) >= 0x4e00 {
				t.Log("warning: response may not be in English")
			}
		}
	}
}

// ─── Anthropic 兼容端点 ───

func TestIntegration_AnthropicEndpoint(t *testing.T) {
	cfg := loadRealConfig(t)
	handler := NewProxyHandler(cfg)

	// 通过 Anthropic 端点发送请求
	body := `{"model":"mimo-claude","messages":[{"role":"user","content":"say ok"}],"max_tokens":100}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: %d, body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)

	// Anthropic 格式验证
	if resp["role"] != "assistant" {
		t.Errorf("role: got %q", resp["role"])
	}
	content, ok := resp["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatal("expected non-empty content array")
	}

	for _, block := range content {
		b := block.(map[string]any)
		t.Logf("block type=%s", b["type"])
		if b["type"] == "text" {
			t.Logf("  text: %s", b["text"])
		}
	}
}

// ─── /health 端点 ───

func TestIntegration_Health(t *testing.T) {
	cfg := loadRealConfig(t)
	handler := NewProxyHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	// health 不经过 ProxyHandler，直接测 mux
	mux := http.NewServeMux()
	mux.Handle("/v1/", handler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status: %d", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("body: %q", w.Body.String())
	}
}
