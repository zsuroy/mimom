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

// ResponsesHandler 处理 /v1/responses 请求，转换为 Chat Completions 格式转发。
type ResponsesHandler struct {
	cfg    *Config
	client *http.Client
	reason *ReasoningStore
	stats  *Stats
}

func NewResponsesHandler(cfg *Config, reason *ReasoningStore) *ResponsesHandler {
	timeout := time.Duration(cfg.Server.Timeout) * time.Second
	return &ResponsesHandler{
		cfg:    cfg,
		client: &http.Client{Timeout: timeout},
		reason: reason,
	}
}

// responsesRequest 是 OpenAI Responses API 的请求格式。
type responsesRequest struct {
	Model        string          `json:"model"`
	Input        json.RawMessage `json:"input"` // string 或 array
	Instructions string          `json:"instructions"`
	Stream       bool            `json:"stream"`
	Temperature  *float64        `json:"temperature,omitempty"`
	Tools        json.RawMessage `json:"tools,omitempty"`
	MaxTokens    int             `json:"max_output_tokens,omitempty"`
}

// chatRequest 是 Chat Completions 的请求格式。
type chatRequest struct {
	Model       string          `json:"model"`
	Messages    []chatMessage   `json:"messages"`
	Stream      bool            `json:"stream"`
	Temperature *float64        `json:"temperature,omitempty"`
	Tools       json.RawMessage `json:"tools,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (h *ResponsesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	status := 200
	isStream := false
	defer func() {
		duration := time.Since(start)
		log.Printf("%s %s %s %d %s", r.Method, r.URL.Path, r.URL.RawQuery, status, duration.Round(time.Millisecond))
		if h.stats != nil {
			h.stats.RecordRequest(r.Method, r.URL.Path, status, duration, isStream)
		}
	}()

	if r.Method != http.MethodPost {
		status = 405
		http.Error(w, `{"error":{"message":"method not allowed"}}`, 405)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		status = 400
		http.Error(w, `{"error":{"message":"failed to read body"}}`, 400)
		return
	}
	r.Body.Close()

	var req responsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		status = 400
		http.Error(w, `{"error":{"message":"invalid json"}}`, 400)
		return
	}

	// 路由
	backend, realModel, _ := h.cfg.LookupModel(req.Model)
	if backend == nil {
		backend = h.cfg.DefaultBackend()
	}
	if backend == nil {
		status = 502
		http.Error(w, `{"error":{"message":"no backend available"}}`, 502)
		return
	}

	// 转换为 Chat Completions 格式
	chatReq := convertToChatRequest(req, realModel)
	chatBody, _ := json.Marshal(chatReq)

	// ★ 注入缓存的 reasoning_content
	if patched, err := h.reason.InjectReasoning(chatBody); err == nil {
		chatBody = patched
	}

	// 构建后端 URL（转发到 /chat/completions）
	base := strings.TrimRight(backend.BaseURL, "/")
	targetURL := base + "/chat/completions"

	backendReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(chatBody))
	if err != nil {
		status = 500
		http.Error(w, `{"error":{"message":"failed to create request"}}`, 500)
		return
	}
	backendReq.Header.Set("Content-Type", "application/json")
	backendReq.Header.Set("Authorization", "Bearer "+backend.APIKey)

	resp, err := h.client.Do(backendReq)
	if err != nil {
		status = 502
		http.Error(w, `{"error":{"message":"backend request failed"}}`, 502)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		status = resp.StatusCode
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
		return
	}

	if req.Stream {
		isStream = true
		status = 200
		h.handleStream(w, resp, req.Model)
	} else {
		status = 200
		h.handleNonStream(w, resp, req.Model)
	}
}

// handleNonStream 将 Chat Completions 响应转换为 Responses API 格式。
func (h *ResponsesHandler) handleNonStream(w http.ResponseWriter, resp *http.Response, model string) {
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, `{"error":{"message":"failed to read response"}}`, 502)
		return
	}

	// 缓存 reasoning_content
	go h.reason.ExtractReasoning(respBody)

	// 解析 Chat Completions 响应
	var chatResp struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		// 解析失败，原样返回
		w.Header().Set("Content-Type", "application/json")
		w.Write(respBody)
		return
	}

	// 构建 Responses API 响应
	output := []map[string]any{}
	if len(chatResp.Choices) > 0 {
		msg := chatResp.Choices[0].Message
		// reasoning 输出项
		if msg.ReasoningContent != "" {
			output = append(output, map[string]any{
				"type":   "reasoning",
				"id":     "rs_" + randomID(),
				"status": "completed",
				"content": []map[string]string{
					{"type": "reasoning_text", "text": msg.ReasoningContent},
				},
			})
		}
		// message 输出项
		output = append(output, map[string]any{
			"type":   "message",
			"id":     "msg_" + randomID(),
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]string{
				{"type": "output_text", "text": msg.Content},
			},
		})
	}

	responsesResp := map[string]any{
		"id":     "resp_" + randomID(),
		"object": "response",
		"model":  model,
		"output": output,
		"usage": map[string]int{
			"input_tokens":  chatResp.Usage.PromptTokens,
			"output_tokens": chatResp.Usage.CompletionTokens,
			"total_tokens":  chatResp.Usage.TotalTokens,
		},
		"status": "completed",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(responsesResp)
}

// handleStream 将 Chat Completions SSE 转换为 Responses API SSE 事件。
func (h *ResponsesHandler) handleStream(w http.ResponseWriter, resp *http.Response, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(200)

	flusher, ok := w.(http.Flusher)
	if !ok {
		io.Copy(w, resp.Body)
		return
	}

	respID := "resp_" + randomID()
	msgID := "msg_" + randomID()

	// 发送 response.created
	sendEvent(w, flusher, "response.created", map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":     respID,
			"object": "response",
			"model":  model,
			"status": "in_progress",
			"output": []any{},
		},
	})

	// 发送 response.output_item.added（reasoning）
	reasoningID := "rs_" + randomID()
	sendEvent(w, flusher, "response.output_item.added", map[string]any{
		"type":  "response.output_item.added",
		"index": 0,
		"item": map[string]any{
			"type":    "reasoning",
			"id":      reasoningID,
			"status":  "completed",
			"content": []any{},
		},
	})

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	var reasoningChunks []string
	inReasoning := true // 先收集 reasoning

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") || strings.HasSuffix(line, "[DONE]") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var chunk struct {
			Choices []struct {
				Delta struct {
					ReasoningContent *string `json:"reasoning_content"`
					Content          *string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}

		for _, c := range chunk.Choices {
			// reasoning_content delta
			if c.Delta.ReasoningContent != nil && *c.Delta.ReasoningContent != "" {
				reasoningChunks = append(reasoningChunks, *c.Delta.ReasoningContent)
				sendEvent(w, flusher, "response.content_part.added", map[string]any{
					"type":  "response.content_part.added",
					"index": 0,
				})
				sendEvent(w, flusher, "response.reasoning_text.delta", map[string]any{
					"type":  "response.reasoning_text.delta",
					"index": 0,
					"delta": *c.Delta.ReasoningContent,
				})
			}

			// content delta（推理结束后切到 message）
			if c.Delta.Content != nil && *c.Delta.Content != "" {
				if inReasoning {
					// 关闭 reasoning item
					sendEvent(w, flusher, "response.output_item.done", map[string]any{
						"type":  "response.output_item.done",
						"index": 0,
						"item": map[string]any{
							"type":   "reasoning",
							"id":     reasoningID,
							"status": "completed",
							"content": []map[string]string{
								{"type": "reasoning_text", "text": strings.Join(reasoningChunks, "")},
							},
						},
					})
					// 打开 message item
					sendEvent(w, flusher, "response.output_item.added", map[string]any{
						"type":  "response.output_item.added",
						"index": 1,
						"item": map[string]any{
							"type":    "message",
							"id":      msgID,
							"role":    "assistant",
							"status":  "in_progress",
							"content": []any{},
						},
					})
					inReasoning = false
				}
				sendEvent(w, flusher, "response.output_text.delta", map[string]any{
					"type":  "response.output_text.delta",
					"index": 1,
					"delta": *c.Delta.Content,
				})
			}
		}
	}

	// 如果只有 reasoning 没有 content，关闭 reasoning item
	if inReasoning && len(reasoningChunks) > 0 {
		sendEvent(w, flusher, "response.output_item.done", map[string]any{
			"type":  "response.output_item.done",
			"index": 0,
			"item": map[string]any{
				"type":   "reasoning",
				"id":     reasoningID,
				"status": "completed",
				"content": []map[string]string{
					{"type": "reasoning_text", "text": strings.Join(reasoningChunks, "")},
				},
			},
		})
	}

	// 关闭 message item
	if !inReasoning {
		sendEvent(w, flusher, "response.output_item.done", map[string]any{
			"type":  "response.output_item.done",
			"index": 1,
			"item": map[string]any{
				"type":    "message",
				"id":      msgID,
				"role":    "assistant",
				"status":  "completed",
				"content": []any{},
			},
		})
	}

	// response.completed
	sendEvent(w, flusher, "response.completed", map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":     respID,
			"object": "response",
			"model":  model,
			"status": "completed",
			"output": []any{},
		},
	})

	// 缓存 reasoning
	if len(reasoningChunks) > 0 {
		h.reason.CacheStreamReasoning("stream:"+respID, strings.Join(reasoningChunks, ""))
	}
}

func sendEvent(w http.ResponseWriter, flusher http.Flusher, event string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	flusher.Flush()
}

func randomID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano()%1e12)
}

// convertToChatRequest 将 Responses API 请求转换为 Chat Completions 请求。
func convertToChatRequest(req responsesRequest, realModel string) chatRequest {
	model := req.Model
	if realModel != "" {
		model = realModel
	}

	messages := []chatMessage{}

	// instructions → system message
	if req.Instructions != "" {
		messages = append(messages, chatMessage{Role: "system", Content: req.Instructions})
	}

	// input → messages
	if len(req.Input) > 0 {
		// 尝试解析为 string
		var inputStr string
		if err := json.Unmarshal(req.Input, &inputStr); err == nil {
			messages = append(messages, chatMessage{Role: "user", Content: inputStr})
		} else {
			// 解析为 array
			var inputArr []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(req.Input, &inputArr); err == nil {
				for _, item := range inputArr {
					messages = append(messages, chatMessage{Role: item.Role, Content: item.Content})
				}
			}
		}
	}

	return chatRequest{
		Model:       model,
		Messages:    messages,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		Tools:       req.Tools,
		MaxTokens:   req.MaxTokens,
	}
}
