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

// chatMessage 用 map 保留所有字段，支持 function calling 的 tool_calls / tool 消息。
type chatMessage = map[string]any

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
				Content          string         `json:"content"`
				ReasoningContent string         `json:"reasoning_content"`
				ToolCalls        []toolCallItem `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
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
		// function_call 输出项（tool_calls）
		for _, tc := range msg.ToolCalls {
			output = append(output, map[string]any{
				"type":      "function_call",
				"id":        "fc_" + randomID(),
				"call_id":   tc.ID,
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
			})
		}
		// message 输出项（仅当有文本内容时）
		if msg.Content != "" || (len(msg.ToolCalls) == 0 && msg.ReasoningContent == "") {
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

type toolCallItem struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type toolCallDelta struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func closeReasoningItem(w http.ResponseWriter, flusher http.Flusher, reasoningID string, index int, reasoningChunks []string) {
	sendEvent(w, flusher, "response.output_item.done", map[string]any{
		"type":  "response.output_item.done",
		"index": index,
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
	var contentChunks []string
	inReasoning := true  // 先收集 reasoning
	outputIndex := 0    // 跟踪输出项索引
	inFunctionCall := false
	messageOpened := false
	currentFCID := ""
	currentFCName := ""
	currentFCArgs := ""

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") || strings.HasSuffix(line, "[DONE]") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var chunk struct {
			Choices []struct {
				Delta struct {
					ReasoningContent *string       `json:"reasoning_content"`
					Content          *string       `json:"content"`
					ToolCalls        []toolCallDelta `json:"tool_calls"`
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
				sendEvent(w, flusher, "response.reasoning_text.delta", map[string]any{
					"type":  "response.reasoning_text.delta",
					"index": outputIndex,
					"delta": *c.Delta.ReasoningContent,
				})
			}

			// tool_calls delta
			for _, tc := range c.Delta.ToolCalls {
				if tc.ID != "" && !inFunctionCall {
					// 新的 function_call 开始
					if inReasoning && len(reasoningChunks) > 0 {
						closeReasoningItem(w, flusher, reasoningID, outputIndex, reasoningChunks)
						outputIndex++
						inReasoning = false
					}
					inFunctionCall = true
					currentFCID = tc.ID
					if tc.Function.Name != "" {
						currentFCName = tc.Function.Name
					}
					currentFCArgs = ""
					sendEvent(w, flusher, "response.output_item.added", map[string]any{
						"type":  "response.output_item.added",
						"index": outputIndex,
						"item": map[string]any{
							"type":    "function_call",
							"id":      "fc_" + randomID(),
							"call_id": currentFCID,
							"name":    currentFCName,
							"status":  "in_progress",
						},
					})
				}
				if tc.Function.Name != "" && currentFCName == "" {
					currentFCName = tc.Function.Name
				}
				if tc.Function.Arguments != "" {
					currentFCArgs += tc.Function.Arguments
					sendEvent(w, flusher, "response.function_call_arguments.delta", map[string]any{
						"type":  "response.function_call_arguments.delta",
						"index": outputIndex,
						"delta": tc.Function.Arguments,
					})
				}
			}

			// function_call 完成（通过 finish_reason 或 content 切换）
			if inFunctionCall && (c.FinishReason != nil && *c.FinishReason == "tool_calls") {
				sendEvent(w, flusher, "response.output_item.done", map[string]any{
					"type":  "response.output_item.done",
					"index": outputIndex,
					"item": map[string]any{
						"type":      "function_call",
						"id":        "fc_" + randomID(),
						"call_id":   currentFCID,
						"name":      currentFCName,
						"arguments": currentFCArgs,
						"status":    "completed",
					},
				})
				outputIndex++
				inFunctionCall = false
				currentFCID = ""
				currentFCName = ""
				currentFCArgs = ""
			}

			// content delta（推理结束后切到 message）
			if c.Delta.Content != nil && *c.Delta.Content != "" {
				if inFunctionCall {
					// function_call 结束
					sendEvent(w, flusher, "response.output_item.done", map[string]any{
						"type":  "response.output_item.done",
						"index": outputIndex,
						"item": map[string]any{
							"type":      "function_call",
							"id":        "fc_" + randomID(),
							"call_id":   currentFCID,
							"name":      currentFCName,
							"arguments": currentFCArgs,
							"status":    "completed",
						},
					})
					outputIndex++
					inFunctionCall = false
					currentFCID = ""
					currentFCName = ""
					currentFCArgs = ""
				}
				if inReasoning {
					// 关闭 reasoning item
					closeReasoningItem(w, flusher, reasoningID, outputIndex, reasoningChunks)
					outputIndex++
					inReasoning = false
				}
				if !messageOpened {
					messageOpened = true
					sendEvent(w, flusher, "response.output_item.added", map[string]any{
						"type":  "response.output_item.added",
						"index": outputIndex,
						"item": map[string]any{
							"type":    "message",
							"id":      msgID,
							"role":    "assistant",
							"status":  "in_progress",
							"content": []any{},
						},
					})
				}
				sendEvent(w, flusher, "response.output_text.delta", map[string]any{
					"type":  "response.output_text.delta",
					"index": outputIndex,
					"delta": *c.Delta.Content,
				})
				contentChunks = append(contentChunks, *c.Delta.Content)
			}

			// finish_reason: stop — 关闭所有未完成的 item
			if c.FinishReason != nil && *c.FinishReason == "stop" {
				if inReasoning && len(reasoningChunks) > 0 {
					closeReasoningItem(w, flusher, reasoningID, outputIndex, reasoningChunks)
					outputIndex++
					inReasoning = false
				}
				if messageOpened {
					sendEvent(w, flusher, "response.output_item.done", map[string]any{
						"type":  "response.output_item.done",
						"index": outputIndex,
						"item": map[string]any{
							"type":    "message",
							"id":      msgID,
							"role":    "assistant",
							"status":  "completed",
							"content": []map[string]any{
								{"type": "output_text", "text": strings.Join(contentChunks, "")},
							},
						},
					})
					outputIndex++
					messageOpened = false
				}
			}
		}
	}

	// 关闭未完成的 reasoning item
	if inReasoning && len(reasoningChunks) > 0 {
		closeReasoningItem(w, flusher, reasoningID, outputIndex, reasoningChunks)
		outputIndex++
	}

	// 关闭未完成的 function_call item
	if inFunctionCall {
		sendEvent(w, flusher, "response.output_item.done", map[string]any{
			"type":  "response.output_item.done",
			"index": outputIndex,
			"item": map[string]any{
				"type":      "function_call",
				"id":        "fc_" + randomID(),
				"call_id":   currentFCID,
				"name":      currentFCName,
				"arguments": currentFCArgs,
				"status":    "completed",
			},
		})
		outputIndex++
	}

	// 关闭 message item（如果有 content 输出）
	if messageOpened {
		sendEvent(w, flusher, "response.output_item.done", map[string]any{
			"type":  "response.output_item.done",
			"index": outputIndex,
			"item": map[string]any{
				"type":    "message",
				"id":      msgID,
				"role":    "assistant",
				"status":  "completed",
				"content": []map[string]any{
					{"type": "output_text", "text": strings.Join(contentChunks, "")},
				},
			},
		})
	} else if len(contentChunks) > 0 {
		// 没有 message item 但有 content（异常情况），补发
		sendEvent(w, flusher, "response.output_item.added", map[string]any{
			"type":  "response.output_item.added",
			"index": outputIndex,
			"item": map[string]any{
				"type":    "message",
				"id":      msgID,
				"role":    "assistant",
				"status":  "in_progress",
				"content": []any{},
			},
		})
		sendEvent(w, flusher, "response.output_item.done", map[string]any{
			"type":  "response.output_item.done",
			"index": outputIndex,
			"item": map[string]any{
				"type":    "message",
				"id":      msgID,
				"role":    "assistant",
				"status":  "completed",
				"content": []map[string]any{
					{"type": "output_text", "text": strings.Join(contentChunks, "")},
				},
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
	fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()

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
		messages = append(messages, chatMessage{"role": "system", "content": req.Instructions})
	}

	// input → messages
	if len(req.Input) > 0 {
		// 尝试解析为 string
		var inputStr string
		if err := json.Unmarshal(req.Input, &inputStr); err == nil {
			messages = append(messages, chatMessage{"role": "user", "content": inputStr})
		} else {
			// 解析为 array（可能包含 function_call / function_call_output）
			var inputArr []map[string]any
			if err := json.Unmarshal(req.Input, &inputArr); err == nil {
				for _, item := range inputArr {
					msg := convertInputItemToMessage(item)
					if msg != nil {
						messages = append(messages, msg)
					}
				}
			}
		}
	}

	return chatRequest{
		Model:       model,
		Messages:    messages,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		Tools:       convertToolsToChatFormat(req.Tools),
		MaxTokens:   req.MaxTokens,
	}
}

// convertInputItemToMessage 将 Responses API input 项转换为 Chat Completions message。
func convertInputItemToMessage(item map[string]any) chatMessage {
	// function_call_output → tool message
	if typ, _ := item["type"].(string); typ == "function_call_output" {
		callID, _ := item["call_id"].(string)
		output, _ := item["output"].(string)
		return chatMessage{"role": "tool", "tool_call_id": callID, "content": output}
	}

	// function_call → assistant message with tool_calls
	if typ, _ := item["type"].(string); typ == "function_call" {
		callID, _ := item["call_id"].(string)
		name, _ := item["name"].(string)
		args, _ := item["arguments"].(string)
		return chatMessage{
			"role": "assistant",
			"tool_calls": []map[string]any{
				{
					"id":   callID,
					"type": "function",
					"function": map[string]any{
						"name":      name,
						"arguments": args,
					},
				},
			},
		}
	}

	// 普通 message（user / assistant / system）
	role, _ := item["role"].(string)
	if role == "" {
		return nil
	}

	// developer → system（Codex 使用 developer 角色，MiMo 不支持）
	if role == "developer" {
		role = "system"
	}

	msg := chatMessage{"role": role}

	// assistant 消息可能带 tool_calls（从之前的响应透传回来）
	if tc, ok := item["tool_calls"]; ok {
		// 转换 tool_calls 到 Chat Completions 格式
		msg["tool_calls"] = convertToolCallsToChatFormat(tc)
		// MiMo 要求 content 字段存在，即使是空字符串
		msg["content"] = ""
		return msg
	}

	// content 可能是 string 或 array（Codex 发送 input_text 数组）
	if content, ok := item["content"]; ok {
		msg["content"] = extractTextContent(content)
	}

	return msg
}

// extractTextContent 从 content 值中提取纯文本。
// Codex 发送 content 为 [{"type":"input_text","text":"..."}] 格式，
// 也可能包含 {"type":"thinking"} 等 MiMo 不支持的内容类型。
func extractTextContent(content any) any {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				// 只提取 text 类型，跳过 thinking 等不支持的类型
				if typ, _ := m["type"].(string); typ == "text" || typ == "input_text" || typ == "output_text" {
					if text, ok := m["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "")
		}
		// 没有可用的文本部分，返回空字符串避免传递不支持的格式
		return ""
	}
	return content
}

// convertToolCallsToChatFormat 将 Responses API 的 tool_calls 转换为 Chat Completions 格式。
// Responses API: {"type":"function","name":"fn","arguments":"..."}
// Chat Completions: {"type":"function","function":{"name":"fn","arguments":"..."}}
func convertToolCallsToChatFormat(toolCalls any) any {
	tcArr, ok := toolCalls.([]any)
	if !ok {
		return toolCalls
	}

	result := make([]map[string]any, 0, len(tcArr))
	for _, tc := range tcArr {
		tcMap, ok := tc.(map[string]any)
		if !ok {
			continue
		}
		// 已经是 Chat Completions 格式（有 function 字段）
		if _, ok := tcMap["function"]; ok {
			result = append(result, tcMap)
			continue
		}
		// Responses API 格式：需要包装 function 字段
		name, _ := tcMap["name"].(string)
		if name == "" {
			continue
		}
		fn := map[string]any{"name": name}
		if args, ok := tcMap["arguments"].(string); ok {
			fn["arguments"] = args
		}
		entry := map[string]any{
			"type":     "function",
			"function": fn,
		}
		if id, ok := tcMap["id"].(string); ok {
			entry["id"] = id
		}
		if callID, ok := tcMap["call_id"].(string); ok {
			entry["id"] = callID
		}
		result = append(result, entry)
	}
	return result
}

// convertToolsToChatFormat 将 Responses API 的 tools 格式转换为 Chat Completions 格式。
// 只保留 type=function 的工具，跳过 custom/tool_search 等不支持的类型。
// Responses API: {"type":"function","name":"fn","parameters":{...}}
// Chat Completions: {"type":"function","function":{"name":"fn","parameters":{...}}}
func convertToolsToChatFormat(tools json.RawMessage) json.RawMessage {
	if len(tools) == 0 {
		return nil
	}

	var toolList []json.RawMessage
	if err := json.Unmarshal(tools, &toolList); err != nil {
		return tools
	}

	converted := make([]json.RawMessage, 0, len(toolList))
	for _, tool := range toolList {
		var t struct {
			Type        string          `json:"type"`
			Name        string          `json:"name,omitempty"`
			Description string          `json:"description,omitempty"`
			Parameters  json.RawMessage `json:"parameters,omitempty"`
			Function    json.RawMessage `json:"function,omitempty"`
		}
		if err := json.Unmarshal(tool, &t); err != nil {
			continue
		}

		// 只处理 function 类型的工具，跳过 custom/tool_search 等
		if t.Type != "function" {
			continue
		}

		// 已经是 Chat Completions 格式（有 function 字段），直接保留
		if t.Function != nil {
			converted = append(converted, tool)
			continue
		}

		// Responses API 格式：需要包装 function 字段
		if t.Name != "" {
			fn := map[string]any{
				"name": t.Name,
			}
			if t.Description != "" {
				fn["description"] = t.Description
			}
			if t.Parameters != nil {
				fn["parameters"] = json.RawMessage(t.Parameters)
			}
			wrapped := map[string]any{
				"type":     "function",
				"function": fn,
			}
			b, _ := json.Marshal(wrapped)
			converted = append(converted, b)
		}
	}

	if len(converted) == 0 {
		return nil
	}
	result, _ := json.Marshal(converted)
	return json.RawMessage(result)
}
