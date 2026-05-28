package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── ResponsesHandler HTTP 测试 ───

func newTestResponsesHandler(backendURL string) *ResponsesHandler {
	cfg := &Config{
		Server: ServerConfig{Port: 8080, Timeout: 5},
		Backends: map[string]BackendDef{
			"test": {
				BaseURL: backendURL,
				APIKey:  "test-key",
				Models:  map[string]string{"mimo-test": "mimo-v2"},
			},
		},
	}
	reason := NewReasoningStore()
	return NewResponsesHandler(cfg, reason)
}

func TestResponsesHandler_MethodNotAllowed(t *testing.T) {
	h := newTestResponsesHandler("http://localhost:1")
	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 405 {
		t.Errorf("status: got %d, want 405", w.Code)
	}
}

func TestResponsesHandler_InvalidJSON(t *testing.T) {
	h := newTestResponsesHandler("http://localhost:1")
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("{bad json"))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestResponsesHandler_NoBackend(t *testing.T) {
	cfg := &Config{
		Server:   ServerConfig{Port: 8080, Timeout: 5},
		Backends: map[string]BackendDef{},
	}
	reason := NewReasoningStore()
	h := NewResponsesHandler(cfg, reason)

	body := `{"model":"unknown","input":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 502 {
		t.Errorf("status: got %d, want 502", w.Code)
	}
}

func TestResponsesHandler_NonStream_Success(t *testing.T) {
	// 模拟后端 Chat Completions 服务
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("auth: got %q", r.Header.Get("Authorization"))
		}

		// 读取并验证请求
		body, _ := io.ReadAll(r.Body)
		var req chatRequest
		json.Unmarshal(body, &req)
		if req.Model != "mimo-v2" {
			t.Errorf("model: got %q, want %q", req.Model, "mimo-v2")
		}

		// 返回 Chat Completions 响应
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"role":    "assistant",
						"content": "Hello from backend",
					},
				},
			},
			"usage": map[string]int{
				"prompt_tokens":     10,
				"completion_tokens": 5,
				"total_tokens":      15,
			},
		})
	}))
	defer backend.Close()

	h := newTestResponsesHandler(backend.URL)
	body := `{"model":"mimo-test","input":"Hello"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["object"] != "response" {
		t.Errorf("object: got %q", resp["object"])
	}
	if resp["model"] != "mimo-test" {
		t.Errorf("model: got %q", resp["model"])
	}
	if resp["status"] != "completed" {
		t.Errorf("status: got %q", resp["status"])
	}

	output, ok := resp["output"].([]any)
	if !ok || len(output) == 0 {
		t.Fatal("output: expected non-empty array")
	}

	// 最后一个 output 应该是 message
	last := output[len(output)-1].(map[string]any)
	if last["type"] != "message" {
		t.Errorf("output type: got %q, want %q", last["type"], "message")
	}
	content := last["content"].([]any)[0].(map[string]any)
	if content["text"] != "Hello from backend" {
		t.Errorf("text: got %q", content["text"])
	}
}

func TestResponsesHandler_NonStream_WithReasoning(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{
						"role":              "assistant",
						"content":           "The answer is 42",
						"reasoning_content": "Let me think... 6*7=42",
					},
				},
			},
			"usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 10, "total_tokens": 15},
		})
	}))
	defer backend.Close()

	h := newTestResponsesHandler(backend.URL)
	body := `{"model":"mimo-test","input":"what is 6*7?"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: got %d", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)

	output := resp["output"].([]any)
	if len(output) < 2 {
		t.Fatalf("output: got %d items, want >=2", len(output))
	}

	// 第一个应该是 reasoning
	reasoning := output[0].(map[string]any)
	if reasoning["type"] != "reasoning" {
		t.Errorf("output[0] type: got %q, want %q", reasoning["type"], "reasoning")
	}
	rc := reasoning["content"].([]any)[0].(map[string]any)
	if rc["text"] != "Let me think... 6*7=42" {
		t.Errorf("reasoning text: got %q", rc["text"])
	}

	// 第二个应该是 message
	msg := output[1].(map[string]any)
	if msg["type"] != "message" {
		t.Errorf("output[1] type: got %q, want %q", msg["type"], "message")
	}
}

func TestResponsesHandler_NonStream_BackendError(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"message": "invalid model"},
		})
	}))
	defer backend.Close()

	h := newTestResponsesHandler(backend.URL)
	body := `{"model":"mimo-test","input":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestResponsesHandler_Stream_Success(t *testing.T) {
	// 模拟 SSE 后端
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)

		flusher := w.(http.Flusher)

		// 先发 reasoning chunks
		rc1 := "Let me "
		rc2 := "think..."
		data1, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]any{"reasoning_content": rc1}}},
		})
		data2, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]any{"reasoning_content": rc2}}},
		})

		w.Write([]byte("data: " + string(data1) + "\n\n"))
		flusher.Flush()
		w.Write([]byte("data: " + string(data2) + "\n\n"))
		flusher.Flush()

		// 然后发 content chunks
		c1 := "The answer "
		c2 := "is 42."
		data3, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]any{"content": c1}}},
		})
		data4, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]any{"content": c2}}},
		})

		w.Write([]byte("data: " + string(data3) + "\n\n"))
		flusher.Flush()
		w.Write([]byte("data: " + string(data4) + "\n\n"))
		flusher.Flush()

		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer backend.Close()

	h := newTestResponsesHandler(backend.URL)
	body := `{"model":"mimo-test","input":"what is 6*7?","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: got %d", w.Code)
	}

	events := w.Body.String()

	// 验证关键 SSE 事件存在
	if !strings.Contains(events, "event: response.created") {
		t.Error("missing response.created event")
	}
	if !strings.Contains(events, "event: response.completed") {
		t.Error("missing response.completed event")
	}
	if !strings.Contains(events, "response.reasoning_text.delta") {
		t.Error("missing reasoning delta events")
	}
	if !strings.Contains(events, "response.output_text.delta") {
		t.Error("missing output text delta events")
	}
	if !strings.Contains(events, "response.output_item.done") {
		t.Error("missing output_item.done events")
	}

	// 验证 reasoning delta 内容
	if !strings.Contains(events, `"delta":"Let me "`) {
		t.Error("missing first reasoning chunk")
	}
	if !strings.Contains(events, `"delta":"think..."`) {
		t.Error("missing second reasoning chunk")
	}

	// 验证 content delta 内容
	if !strings.Contains(events, `"delta":"The answer "`) {
		t.Error("missing first content chunk")
	}
	if !strings.Contains(events, `"delta":"is 42."`) {
		t.Error("missing second content chunk")
	}
}

func TestResponsesHandler_Stream_ContentOnly(t *testing.T) {
	// 没有 reasoning，只有 content 的流
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)

		data, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]any{"content": "Hello!"}}},
		})
		w.Write([]byte("data: " + string(data) + "\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer backend.Close()

	h := newTestResponsesHandler(backend.URL)
	body := `{"model":"mimo-test","input":"hi","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: got %d", w.Code)
	}

	events := w.Body.String()
	if !strings.Contains(events, `"delta":"Hello!"`) {
		t.Error("missing content delta")
	}
	// 没有 reasoning 时不应发送 reasoning 事件
	if strings.Contains(events, "response.reasoning_text.delta") {
		t.Error("unexpected reasoning events when no reasoning content")
	}
}

func TestResponsesHandler_ModelMapping(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req chatRequest
		json.Unmarshal(body, &req)

		// 验证 model 被替换为后端模型名
		if req.Model != "mimo-v2" {
			t.Errorf("model: got %q, want %q", req.Model, "mimo-v2")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]int{},
		})
	}))
	defer backend.Close()

	h := newTestResponsesHandler(backend.URL)
	body := `{"model":"mimo-test","input":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: got %d", w.Code)
	}
}

func TestResponsesHandler_InstructionsToSystem(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req chatRequest
		json.Unmarshal(body, &req)

		if len(req.Messages) == 0 {
			t.Fatal("no messages")
		}
		if req.Messages[0].Role != "system" {
			t.Errorf("first message role: got %q, want %q", req.Messages[0].Role, "system")
		}
		if req.Messages[0].Content != "Be concise." {
			t.Errorf("system content: got %q", req.Messages[0].Content)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]int{},
		})
	}))
	defer backend.Close()

	h := newTestResponsesHandler(backend.URL)
	body := `{"model":"mimo-test","input":"hi","instructions":"Be concise."}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: got %d", w.Code)
	}
}

func TestResponsesHandler_UsageMapping(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
			"usage": map[string]int{
				"prompt_tokens":     100,
				"completion_tokens": 50,
				"total_tokens":      150,
			},
		})
	}))
	defer backend.Close()

	h := newTestResponsesHandler(backend.URL)
	body := `{"model":"mimo-test","input":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)

	usage := resp["usage"].(map[string]any)
	if int(usage["input_tokens"].(float64)) != 100 {
		t.Errorf("input_tokens: got %v", usage["input_tokens"])
	}
	if int(usage["output_tokens"].(float64)) != 50 {
		t.Errorf("output_tokens: got %v", usage["output_tokens"])
	}
	if int(usage["total_tokens"].(float64)) != 150 {
		t.Errorf("total_tokens: got %v", usage["total_tokens"])
	}
}

func TestResponsesHandler_ResponseIDFormat(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]int{},
		})
	}))
	defer backend.Close()

	h := newTestResponsesHandler(backend.URL)
	body := `{"model":"mimo-test","input":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)

	id, ok := resp["id"].(string)
	if !ok || !strings.HasPrefix(id, "resp_") {
		t.Errorf("id: got %q, want prefix %q", id, "resp_")
	}
}

func TestResponsesHandler_FallbackBackend(t *testing.T) {
	// 使用不在 models 映射中的 model，应回退到默认后端
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req chatRequest
		json.Unmarshal(body, &req)

		// 未映射的 model 不应被替换
		if req.Model != "unknown-model" {
			t.Errorf("model: got %q, want %q", req.Model, "unknown-model")
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "ok"}}},
			"usage":   map[string]int{},
		})
	}))
	defer backend.Close()

	h := newTestResponsesHandler(backend.URL)
	body := `{"model":"unknown-model","input":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
}

// ─── ProxyHandler 测试 ───

func TestProxyHandler_AuthRequired(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{Port: 8080, APIKey: "secret", Timeout: 5},
		Backends: map[string]BackendDef{
			"test": {BaseURL: "http://localhost:1", APIKey: "k", Models: map[string]string{}},
		},
	}
	h := NewProxyHandler(cfg)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	// 不带 Authorization header
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Errorf("status: got %d, want 401", w.Code)
	}
}

func TestProxyHandler_AuthPass(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer backend.Close()

	cfg := &Config{
		Server: ServerConfig{Port: 8080, APIKey: "secret", Timeout: 5},
		Backends: map[string]BackendDef{
			"test": {BaseURL: backend.URL, APIKey: "bk", Models: map[string]string{}},
		},
	}
	h := NewProxyHandler(cfg)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status: got %d, want 200", w.Code)
	}
}

func TestProxyHandler_ModelRouting(t *testing.T) {
	var receivedModel string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(body, &req)
		receivedModel, _ = req["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer backend.Close()

	cfg := &Config{
		Server: ServerConfig{Port: 8080, Timeout: 5},
		Backends: map[string]BackendDef{
			"test": {
				BaseURL: backend.URL,
				APIKey:  "k",
				Models:  map[string]string{"client-m": "backend-m"},
			},
		},
	}
	h := NewProxyHandler(cfg)

	body := `{"model":"client-m","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if receivedModel != "backend-m" {
		t.Errorf("model: got %q, want %q", receivedModel, "backend-m")
	}
}

func TestProxyHandler_PathSuffix(t *testing.T) {
	var receivedPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer backend.Close()

	cfg := &Config{
		Server: ServerConfig{Port: 8080, Timeout: 5},
		Backends: map[string]BackendDef{
			"test": {BaseURL: backend.URL + "/v1", APIKey: "k", Models: map[string]string{}},
		},
	}
	h := NewProxyHandler(cfg)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if receivedPath != "/v1/chat/completions" {
		t.Errorf("path: got %q, want %q", receivedPath, "/v1/chat/completions")
	}
}

// ─── randomID ───

func TestRandomID_Format(t *testing.T) {
	id := randomID()
	if !strings.HasPrefix(id, "") {
		t.Error("expected non-empty id")
	}
	// ID should be numeric
	for _, c := range id {
		if c < '0' || c > '9' {
			t.Errorf("id contains non-digit: %q", id)
			break
		}
	}
}

// ─── ResponsesHandler 边界情况 ───

func TestResponsesHandler_NonStream_EmptyChoices(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{},
			"usage":   map[string]int{},
		})
	}))
	defer backend.Close()

	h := newTestResponsesHandler(backend.URL)
	body := `{"model":"mimo-test","input":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)

	output := resp["output"].([]any)
	if len(output) != 0 {
		t.Errorf("output: got %d items, want 0 for empty choices", len(output))
	}
}

func TestResponsesHandler_Stream_ReasoningOnly(t *testing.T) {
	// 只有 reasoning 没有 content 的流
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)

		data, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]any{"reasoning_content": "thinking..."}}},
		})
		w.Write([]byte("data: " + string(data) + "\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer backend.Close()

	h := newTestResponsesHandler(backend.URL)
	body := `{"model":"mimo-test","input":"hi","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: got %d", w.Code)
	}

	events := w.Body.String()
	if !strings.Contains(events, "response.reasoning_text.delta") {
		t.Error("missing reasoning delta events")
	}
	// 没有 content 时不应有 output_text.delta
	if strings.Contains(events, "response.output_text.delta") {
		t.Error("unexpected output_text.delta when no content")
	}
}

func TestResponsesHandler_Stream_MultipleContentChunks(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)

		chunks := []string{"Hello", " ", "world", "!"}
		for _, c := range chunks {
			data, _ := json.Marshal(map[string]any{
				"choices": []map[string]any{{"delta": map[string]any{"content": c}}},
			})
			w.Write([]byte("data: " + string(data) + "\n\n"))
			flusher.Flush()
		}
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer backend.Close()

	h := newTestResponsesHandler(backend.URL)
	body := `{"model":"mimo-test","input":"hi","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	events := w.Body.String()
	for _, chunk := range []string{"Hello", " ", "world", "!"} {
		if !strings.Contains(events, `"delta":"`+chunk+`"`) {
			t.Errorf("missing chunk %q", chunk)
		}
	}
}

func TestResponsesHandler_NonStream_MultipleChoices(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "first"}},
				{"message": map[string]any{"role": "assistant", "content": "second"}},
			},
			"usage": map[string]int{},
		})
	}))
	defer backend.Close()

	h := newTestResponsesHandler(backend.URL)
	body := `{"model":"mimo-test","input":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)

	output := resp["output"].([]any)
	// 只取第一个 choice
	if len(output) != 1 {
		t.Errorf("output: got %d items, want 1 (first choice only)", len(output))
	}
}

func TestResponsesHandler_BackendConnectionRefused(t *testing.T) {
	h := newTestResponsesHandler("http://127.0.0.1:1") // 端口 1 会连接失败
	body := `{"model":"mimo-test","input":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 502 {
		t.Errorf("status: got %d, want 502", w.Code)
	}
}

func TestResponsesHandler_NonStream_InvalidBackendResponse(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not json`))
	}))
	defer backend.Close()

	h := newTestResponsesHandler(backend.URL)
	body := `{"model":"mimo-test","input":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	// 解析失败时原样返回
	if w.Code != 200 {
		t.Errorf("status: got %d, want 200", w.Code)
	}
}

func TestResponsesHandler_Stream_SkipsInvalidLines(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)

		// 发送一些无效行
		w.Write([]byte("invalid line\n"))
		w.Write([]byte("\n"))

		data, _ := json.Marshal(map[string]any{
			"choices": []map[string]any{{"delta": map[string]any{"content": "ok"}}},
		})
		w.Write([]byte("data: " + string(data) + "\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer backend.Close()

	h := newTestResponsesHandler(backend.URL)
	body := `{"model":"mimo-test","input":"hi","stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"delta":"ok"`) {
		t.Error("missing content delta after invalid lines")
	}
}

// ─── replaceModelField ───

func TestReplaceModelField(t *testing.T) {
	body := []byte(`{"model":"old","messages":[]}`)
	patched, err := replaceModelField(body, "new")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(patched, &m)
	if m["model"] != "new" {
		t.Errorf("model: got %q, want %q", m["model"], "new")
	}
}

func TestReplaceModelField_InvalidJSON(t *testing.T) {
	_, err := replaceModelField([]byte("{bad"), "new")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// ─── lastAssistantHash ───

func TestProxyHandler_LastAssistantHash(t *testing.T) {
	cfg := &Config{
		Server:   ServerConfig{Port: 8080, Timeout: 5},
		Backends: map[string]BackendDef{"t": {BaseURL: "http://x", APIKey: "k", Models: map[string]string{}}},
	}
	h := NewProxyHandler(cfg)

	body := []byte(`{"messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"hello"}
	]}`)
	hash := h.lastAssistantHash(body)
	if hash == "" {
		t.Error("expected non-empty hash")
	}
}

func TestProxyHandler_LastAssistantHash_NoAssistant(t *testing.T) {
	cfg := &Config{
		Server:   ServerConfig{Port: 8080, Timeout: 5},
		Backends: map[string]BackendDef{"t": {BaseURL: "http://x", APIKey: "k", Models: map[string]string{}}},
	}
	h := NewProxyHandler(cfg)

	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	hash := h.lastAssistantHash(body)
	if hash != "" {
		t.Errorf("expected empty hash, got %q", hash)
	}
}

func TestProxyHandler_LastAssistantHash_InvalidJSON(t *testing.T) {
	cfg := &Config{
		Server:   ServerConfig{Port: 8080, Timeout: 5},
		Backends: map[string]BackendDef{"t": {BaseURL: "http://x", APIKey: "k", Models: map[string]string{}}},
	}
	h := NewProxyHandler(cfg)

	hash := h.lastAssistantHash([]byte("{bad"))
	if hash != "" {
		t.Errorf("expected empty hash for invalid JSON, got %q", hash)
	}
}

// ─── ProxyHandler 流式代理 ───

func TestProxyHandler_Streaming(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher := w.(http.Flusher)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer backend.Close()

	cfg := &Config{
		Server:   ServerConfig{Port: 8080, Timeout: 5},
		Backends: map[string]BackendDef{"t": {BaseURL: backend.URL, APIKey: "k", Models: map[string]string{}}},
	}
	h := NewProxyHandler(cfg)

	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status: got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"content":"hi"`) {
		t.Error("missing streamed content")
	}
}

func TestProxyHandler_AnthropicAuth(t *testing.T) {
	var gotAPIKey, gotVersion string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"content":[{"type":"text","text":"ok"}]}`))
	}))
	defer backend.Close()

	cfg := &Config{
		Server:   ServerConfig{Port: 8080, Timeout: 5},
		Backends: map[string]BackendDef{"t": {Type: "anthropic", BaseURL: backend.URL, APIKey: "ak", Models: map[string]string{}}},
	}
	h := NewProxyHandler(cfg)

	body := `{"model":"claude","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if gotAPIKey != "ak" {
		t.Errorf("x-api-key: got %q, want %q", gotAPIKey, "ak")
	}
	if gotVersion != "2023-06-01" {
		t.Errorf("anthropic-version: got %q", gotVersion)
	}
}

// ─── ProxyHandler 无后端 ───

func TestProxyHandler_NoBackend(t *testing.T) {
	cfg := &Config{
		Server:   ServerConfig{Port: 8080, Timeout: 5},
		Backends: map[string]BackendDef{},
	}
	h := NewProxyHandler(cfg)

	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != 502 {
		t.Errorf("status: got %d, want 502", w.Code)
	}
}
