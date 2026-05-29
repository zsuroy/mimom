package main

import (
	"encoding/json"
	"testing"
)

// ─── convertToChatRequest ───

func TestConvertToChatRequest_StringInput(t *testing.T) {
	input := json.RawMessage(`"Hello, world!"`)
	req := responsesRequest{
		Model:  "mimo-test",
		Input:  input,
		Stream: false,
	}
	chat := convertToChatRequest(req, "")

	if chat.Model != "mimo-test" {
		t.Errorf("model: got %q, want %q", chat.Model, "mimo-test")
	}
	if len(chat.Messages) != 1 {
		t.Fatalf("messages: got %d, want 1", len(chat.Messages))
	}
	if chat.Messages[0]["role"] != "user" {
		t.Errorf("role: got %q, want %q", chat.Messages[0]["role"], "user")
	}
	if chat.Messages[0]["content"] != "Hello, world!" {
		t.Errorf("content: got %q, want %q", chat.Messages[0]["content"], "Hello, world!")
	}
}

func TestConvertToChatRequest_ArrayInput(t *testing.T) {
	input := json.RawMessage(`[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"hello"},
		{"role":"user","content":"how are you?"}
	]`)
	req := responsesRequest{Model: "mimo-test", Input: input}
	chat := convertToChatRequest(req, "")

	if len(chat.Messages) != 3 {
		t.Fatalf("messages: got %d, want 3", len(chat.Messages))
	}
	if chat.Messages[0]["role"] != "user" || chat.Messages[0]["content"] != "hi" {
		t.Errorf("msg[0]: got role=%q content=%q", chat.Messages[0]["role"], chat.Messages[0]["content"])
	}
	if chat.Messages[1]["role"] != "assistant" || chat.Messages[1]["content"] != "hello" {
		t.Errorf("msg[1]: got role=%q content=%q", chat.Messages[1]["role"], chat.Messages[1]["content"])
	}
	if chat.Messages[2]["role"] != "user" || chat.Messages[2]["content"] != "how are you?" {
		t.Errorf("msg[2]: got role=%q content=%q", chat.Messages[2]["role"], chat.Messages[2]["content"])
	}
}

func TestConvertToChatRequest_WithInstructions(t *testing.T) {
	input := json.RawMessage(`"question"`)
	req := responsesRequest{
		Model:        "mimo-test",
		Input:        input,
		Instructions: "You are a helpful assistant.",
	}
	chat := convertToChatRequest(req, "")

	if len(chat.Messages) != 2 {
		t.Fatalf("messages: got %d, want 2", len(chat.Messages))
	}
	if chat.Messages[0]["role"] != "system" {
		t.Errorf("msg[0] role: got %q, want %q", chat.Messages[0]["role"], "system")
	}
	if chat.Messages[0]["content"] != "You are a helpful assistant." {
		t.Errorf("msg[0] content: got %q", chat.Messages[0]["content"])
	}
	if chat.Messages[1]["role"] != "user" {
		t.Errorf("msg[1] role: got %q, want %q", chat.Messages[1]["role"], "user")
	}
}

func TestConvertToChatRequest_RealModelOverride(t *testing.T) {
	req := responsesRequest{Model: "client-model", Input: json.RawMessage(`"hi"`)}
	chat := convertToChatRequest(req, "backend-model")

	if chat.Model != "backend-model" {
		t.Errorf("model: got %q, want %q", chat.Model, "backend-model")
	}
}

func TestConvertToChatRequest_RealModelEmpty(t *testing.T) {
	req := responsesRequest{Model: "client-model", Input: json.RawMessage(`"hi"`)}
	chat := convertToChatRequest(req, "")

	if chat.Model != "client-model" {
		t.Errorf("model: got %q, want %q", chat.Model, "client-model")
	}
}

func TestConvertToChatRequest_PreservesStream(t *testing.T) {
	req := responsesRequest{Model: "m", Input: json.RawMessage(`"x"`), Stream: true}
	chat := convertToChatRequest(req, "")
	if !chat.Stream {
		t.Error("expected Stream=true")
	}
}

func TestConvertToChatRequest_PreservesTemperature(t *testing.T) {
	temp := 0.7
	req := responsesRequest{Model: "m", Input: json.RawMessage(`"x"`), Temperature: &temp}
	chat := convertToChatRequest(req, "")

	if chat.Temperature == nil || *chat.Temperature != 0.7 {
		t.Errorf("temperature: got %v, want 0.7", chat.Temperature)
	}
}

func TestConvertToChatRequest_PreservesTools(t *testing.T) {
	tools := json.RawMessage(`[{"type":"function","function":{"name":"search"}}]`)
	req := responsesRequest{Model: "m", Input: json.RawMessage(`"x"`), Tools: tools}
	chat := convertToChatRequest(req, "")

	if string(chat.Tools) != string(tools) {
		t.Errorf("tools: got %s, want %s", chat.Tools, tools)
	}
}

func TestConvertToolsToChatFormat_ResponsesAPIFormat(t *testing.T) {
	// Responses API format: {"type":"function","name":"fn","parameters":{...}}
	input := json.RawMessage(`[{"type":"function","name":"shell","description":"run a shell command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}}]`)
	result := convertToolsToChatFormat(input)

	var tools []map[string]any
	if err := json.Unmarshal(result, &tools); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("tools count: got %d, want 1", len(tools))
	}
	if tools[0]["type"] != "function" {
		t.Errorf("type: got %v", tools[0]["type"])
	}
	fn, ok := tools[0]["function"].(map[string]any)
	if !ok {
		t.Fatalf("function field missing or not object: %v", tools[0]["function"])
	}
	if fn["name"] != "shell" {
		t.Errorf("function.name: got %v, want shell", fn["name"])
	}
	if fn["description"] != "run a shell command" {
		t.Errorf("function.description: got %v", fn["description"])
	}
}

func TestConvertToolsToChatFormat_AlreadyConverted(t *testing.T) {
	input := json.RawMessage(`[{"type":"function","function":{"name":"search","description":"search web"}}]`)
	result := convertToolsToChatFormat(input)

	if string(result) != string(input) {
		t.Errorf("should pass through unchanged: got %s", result)
	}
}

func TestConvertToolsToChatFormat_EmptyOrNil(t *testing.T) {
	if r := convertToolsToChatFormat(nil); r != nil {
		t.Errorf("nil input: got %s, want nil", r)
	}
	if r := convertToolsToChatFormat(json.RawMessage(`[]`)); r != nil {
		t.Errorf("empty array: got %s, want nil", r)
	}
}

func TestConvertToolsToChatFormat_MixedTools(t *testing.T) {
	input := json.RawMessage(`[
		{"type":"function","name":"shell","parameters":{}},
		{"type":"function","function":{"name":"search"}}
	]`)
	result := convertToolsToChatFormat(input)

	var tools []map[string]any
	json.Unmarshal(result, &tools)
	if len(tools) != 2 {
		t.Fatalf("count: got %d, want 2", len(tools))
	}
	// First should be converted
	if _, ok := tools[0]["function"].(map[string]any); !ok {
		t.Error("tool[0] should have function wrapper")
	}
	// Second should be passed through
	if _, ok := tools[1]["function"].(map[string]any); !ok {
		t.Error("tool[1] should have function wrapper")
	}
}

func TestConvertToolsToChatFormat_FiltersNonFunctionTypes(t *testing.T) {
	input := json.RawMessage(`[
		{"type":"function","function":{"name":"exec_command","parameters":{}}},
		{"type":"custom","name":"apply_patch","description":"edit files","format":{}},
		{"type":"tool_search","description":"search tools","parameters":{}},
		{"type":"function","function":{"name":"view_image","parameters":{}}}
	]`)
	result := convertToolsToChatFormat(input)

	var tools []map[string]any
	json.Unmarshal(result, &tools)
	if len(tools) != 2 {
		t.Fatalf("count: got %d, want 2 (should filter custom and tool_search)", len(tools))
	}
	if tools[0]["type"] != "function" {
		t.Errorf("tool[0] type: got %v, want function", tools[0]["type"])
	}
	if tools[1]["type"] != "function" {
		t.Errorf("tool[1] type: got %v, want function", tools[1]["type"])
	}
}

func TestConvertToolsToChatFormat_AllNonFunction(t *testing.T) {
	input := json.RawMessage(`[
		{"type":"custom","name":"apply_patch"},
		{"type":"tool_search","description":"search"}
	]`)
	result := convertToolsToChatFormat(input)
	if result != nil {
		t.Errorf("expected nil when no function tools, got %s", result)
	}
}

func TestConvertToChatRequest_PreservesMaxTokens(t *testing.T) {
	req := responsesRequest{Model: "m", Input: json.RawMessage(`"x"`), MaxTokens: 1024}
	chat := convertToChatRequest(req, "")

	if chat.MaxTokens != 1024 {
		t.Errorf("max_tokens: got %d, want 1024", chat.MaxTokens)
	}
}

func TestConvertToChatRequest_EmptyInput(t *testing.T) {
	req := responsesRequest{Model: "m"}
	chat := convertToChatRequest(req, "")

	if len(chat.Messages) != 0 {
		t.Errorf("messages: got %d, want 0", len(chat.Messages))
	}
}

func TestConvertToChatRequest_InstructionsOnly(t *testing.T) {
	req := responsesRequest{
		Model:        "m",
		Instructions: "Be brief.",
	}
	chat := convertToChatRequest(req, "")

	if len(chat.Messages) != 1 {
		t.Fatalf("messages: got %d, want 1", len(chat.Messages))
	}
	if chat.Messages[0]["role"] != "system" {
		t.Errorf("role: got %q, want %q", chat.Messages[0]["role"], "system")
	}
}

func TestConvertToChatRequest_MultimodalArrayInput(t *testing.T) {
	input := json.RawMessage(`[
		{"role":"system","content":"You are helpful."},
		{"role":"user","content":"Hello"}
	]`)
	req := responsesRequest{Model: "m", Input: input, Instructions: "System prompt"}
	chat := convertToChatRequest(req, "")

	// instructions 先加，然后是 input 数组
	if len(chat.Messages) != 3 {
		t.Fatalf("messages: got %d, want 3", len(chat.Messages))
	}
	if chat.Messages[0]["role"] != "system" || chat.Messages[0]["content"] != "System prompt" {
		t.Errorf("msg[0]: got role=%q content=%q", chat.Messages[0]["role"], chat.Messages[0]["content"])
	}
	if chat.Messages[1]["role"] != "system" || chat.Messages[1]["content"] != "You are helpful." {
		t.Errorf("msg[1]: got role=%q content=%q", chat.Messages[1]["role"], chat.Messages[1]["content"])
	}
}

// ─── function_call / function_call_output 转换 ───

func TestConvertToChatRequest_FunctionCallOutput(t *testing.T) {
	input := json.RawMessage(`[
		{"role":"user","content":"What is the weather?"},
		{"type":"function_call","call_id":"call_123","name":"get_weather","arguments":"{\"city\":\"Beijing\"}"},
		{"type":"function_call_output","call_id":"call_123","output":"Sunny, 25°C"}
	]`)
	req := responsesRequest{Model: "m", Input: input}
	chat := convertToChatRequest(req, "")

	if len(chat.Messages) != 3 {
		t.Fatalf("messages: got %d, want 3", len(chat.Messages))
	}

	// function_call → assistant message with tool_calls
	msg1 := chat.Messages[1]
	if msg1["role"] != "assistant" {
		t.Errorf("msg[1] role: got %q, want assistant", msg1["role"])
	}
	toolCalls, ok := msg1["tool_calls"].([]map[string]any)
	if !ok {
		t.Fatalf("msg[1] tool_calls: wrong type %T", msg1["tool_calls"])
	}
	if len(toolCalls) != 1 {
		t.Fatalf("tool_calls count: got %d, want 1", len(toolCalls))
	}
	if toolCalls[0]["id"] != "call_123" {
		t.Errorf("tool_call id: got %v", toolCalls[0]["id"])
	}
	fn, _ := toolCalls[0]["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("function name: got %v", fn["name"])
	}

	// function_call_output → tool message
	msg2 := chat.Messages[2]
	if msg2["role"] != "tool" {
		t.Errorf("msg[2] role: got %q, want tool", msg2["role"])
	}
	if msg2["tool_call_id"] != "call_123" {
		t.Errorf("tool_call_id: got %v", msg2["tool_call_id"])
	}
	if msg2["content"] != "Sunny, 25°C" {
		t.Errorf("content: got %v", msg2["content"])
	}
}

func TestConvertToChatRequest_MultipleFunctionCalls(t *testing.T) {
	input := json.RawMessage(`[
		{"role":"user","content":"Do two things"},
		{"type":"function_call","call_id":"c1","name":"fn1","arguments":"{}"},
		{"type":"function_call","call_id":"c2","name":"fn2","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"result1"},
		{"type":"function_call_output","call_id":"c2","output":"result2"}
	]`)
	req := responsesRequest{Model: "m", Input: input}
	chat := convertToChatRequest(req, "")

	if len(chat.Messages) != 5 {
		t.Fatalf("messages: got %d, want 5", len(chat.Messages))
	}

	// First function_call
	if chat.Messages[1]["role"] != "assistant" {
		t.Errorf("msg[1] role: got %q", chat.Messages[1]["role"])
	}
	tc1, _ := chat.Messages[1]["tool_calls"].([]map[string]any)
	if tc1[0]["id"] != "c1" {
		t.Errorf("tc1 id: got %v", tc1[0]["id"])
	}

	// Second function_call
	if chat.Messages[2]["role"] != "assistant" {
		t.Errorf("msg[2] role: got %q", chat.Messages[2]["role"])
	}
	tc2, _ := chat.Messages[2]["tool_calls"].([]map[string]any)
	if tc2[0]["id"] != "c2" {
		t.Errorf("tc2 id: got %v", tc2[0]["id"])
	}

	// Tool results
	if chat.Messages[3]["role"] != "tool" || chat.Messages[3]["tool_call_id"] != "c1" {
		t.Errorf("msg[3]: unexpected %v", chat.Messages[3])
	}
	if chat.Messages[4]["role"] != "tool" || chat.Messages[4]["tool_call_id"] != "c2" {
		t.Errorf("msg[4]: unexpected %v", chat.Messages[4])
	}
}

func TestConvertToChatRequest_AssistantWithToolCalls(t *testing.T) {
	// Assistant message with tool_calls passed through from previous response
	input := json.RawMessage(`[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"","tool_calls":[{"id":"call_abc","type":"function","function":{"name":"shell","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_abc","content":"done"}
	]`)
	req := responsesRequest{Model: "m", Input: input}
	chat := convertToChatRequest(req, "")

	if len(chat.Messages) != 3 {
		t.Fatalf("messages: got %d, want 3", len(chat.Messages))
	}

	msg1 := chat.Messages[1]
	if msg1["role"] != "assistant" {
		t.Errorf("msg[1] role: got %q", msg1["role"])
	}
	// content 设为空字符串（MiMo 要求 content 字段存在）
	if msg1["content"] != "" {
		t.Errorf("msg[1] content: got %q, want empty string", msg1["content"])
	}
	tc, ok := msg1["tool_calls"].([]map[string]any)
	if !ok {
		t.Fatalf("tool_calls: wrong type %T", msg1["tool_calls"])
	}
	if len(tc) != 1 {
		t.Errorf("tool_calls count: got %d", len(tc))
	}
	// 应为 Chat Completions 格式
	fn, _ := tc[0]["function"].(map[string]any)
	if fn["name"] != "shell" {
		t.Errorf("function name: got %v", fn["name"])
	}
}

func TestConvertToChatRequest_AssistantWithToolCallsAndThinking(t *testing.T) {
	// Assistant message with tool_calls AND thinking blocks in content
	// Content replaced with empty string, thinking blocks filtered out
	input := json.RawMessage(`[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"let me think..."}],"tool_calls":[{"id":"call_1","type":"function","function":{"name":"exec","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"ok"}
	]`)
	req := responsesRequest{Model: "m", Input: input}
	chat := convertToChatRequest(req, "")

	if len(chat.Messages) != 3 {
		t.Fatalf("messages: got %d, want 3", len(chat.Messages))
	}

	msg1 := chat.Messages[1]
	if msg1["role"] != "assistant" {
		t.Errorf("msg[1] role: got %q", msg1["role"])
	}
	// content 设为空字符串（thinking 被过滤）
	if msg1["content"] != "" {
		t.Errorf("msg[1] content: got %q, want empty string", msg1["content"])
	}
	tc, ok := msg1["tool_calls"].([]map[string]any)
	if !ok {
		t.Fatalf("tool_calls: wrong type %T", msg1["tool_calls"])
	}
	if len(tc) != 1 {
		t.Errorf("tool_calls count: got %d", len(tc))
	}
}

// ─── convertToolCallsToChatFormat ───

func TestConvertToolCallsToChatFormat_ResponsesAPIFormat(t *testing.T) {
	input := []any{
		map[string]any{
			"id":        "call_123",
			"type":      "function",
			"name":      "get_weather",
			"arguments": `{"city":"Beijing"}`,
		},
	}
	result := convertToolCallsToChatFormat(input)
	tcArr, ok := result.([]map[string]any)
	if !ok {
		t.Fatalf("result type: got %T", result)
	}
	if len(tcArr) != 1 {
		t.Fatalf("count: got %d, want 1", len(tcArr))
	}
	if tcArr[0]["id"] != "call_123" {
		t.Errorf("id: got %v", tcArr[0]["id"])
	}
	fn, _ := tcArr[0]["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Errorf("name: got %v", fn["name"])
	}
	if fn["arguments"] != `{"city":"Beijing"}` {
		t.Errorf("arguments: got %v", fn["arguments"])
	}
}

func TestConvertToolCallsToChatFormat_AlreadyConverted(t *testing.T) {
	input := []any{
		map[string]any{
			"id":   "call_456",
			"type": "function",
			"function": map[string]any{
				"name":      "shell",
				"arguments": "{}",
			},
		},
	}
	result := convertToolCallsToChatFormat(input)
	tcArr, ok := result.([]map[string]any)
	if !ok {
		t.Fatalf("result type: got %T", result)
	}
	if len(tcArr) != 1 {
		t.Fatalf("count: got %d, want 1", len(tcArr))
	}
	fn, _ := tcArr[0]["function"].(map[string]any)
	if fn["name"] != "shell" {
		t.Errorf("name: got %v", fn["name"])
	}
}

func TestConvertToolCallsToChatFormat_NonArray(t *testing.T) {
	// 非数组输入应原样返回
	input := "not an array"
	result := convertToolCallsToChatFormat(input)
	if result != input {
		t.Errorf("should pass through non-array: got %v", result)
	}
}

func TestConvertToolCallsToChatFormat_UsesCallIDAsID(t *testing.T) {
	input := []any{
		map[string]any{
			"call_id":   "call_789",
			"type":      "function",
			"name":      "fn",
			"arguments": "{}",
		},
	}
	result := convertToolCallsToChatFormat(input)
	tcArr, ok := result.([]map[string]any)
	if !ok {
		t.Fatalf("result type: got %T", result)
	}
	if tcArr[0]["id"] != "call_789" {
		t.Errorf("id: got %v, want call_789 (from call_id)", tcArr[0]["id"])
	}
}

func TestConvertInputItemToMessage_DeveloperRole(t *testing.T) {
	item := map[string]any{
		"role":    "developer",
		"content": "You are a coding agent.",
	}
	msg := convertInputItemToMessage(item)
	if msg["role"] != "system" {
		t.Errorf("role: got %q, want system", msg["role"])
	}
	if msg["content"] != "You are a coding agent." {
		t.Errorf("content: got %v", msg["content"])
	}
}

func TestConvertInputItemToMessage_ArrayContent(t *testing.T) {
	item := map[string]any{
		"role": "user",
		"content": []any{
			map[string]any{"type": "input_text", "text": "Hello "},
			map[string]any{"type": "input_text", "text": "world"},
		},
	}
	msg := convertInputItemToMessage(item)
	if msg["role"] != "user" {
		t.Errorf("role: got %q", msg["role"])
	}
	if msg["content"] != "Hello world" {
		t.Errorf("content: got %q, want 'Hello world'", msg["content"])
	}
}

func TestExtractTextContent_ThinkingBlocks(t *testing.T) {
	// MiMo 返回的 thinking blocks 应被过滤
	content := []any{
		map[string]any{"type": "thinking", "thinking": "reasoning..."},
		map[string]any{"type": "output_text", "text": "Hello!"},
	}
	result := extractTextContent(content)
	if result != "Hello!" {
		t.Errorf("got %q, want 'Hello!'", result)
	}
}

func TestExtractTextContent_OnlyThinking(t *testing.T) {
	// 只有 thinking blocks，没有文本 → 返回空字符串
	content := []any{
		map[string]any{"type": "thinking", "thinking": "..."},
	}
	result := extractTextContent(content)
	if result != "" {
		t.Errorf("got %q, want empty string", result)
	}
}

func TestConvertToChatRequest_DeveloperAndArrayContent(t *testing.T) {
	input := json.RawMessage(`[
		{"role":"developer","content":"System prompt"},
		{"role":"user","content":[{"type":"input_text","text":"hi"}]}
	]`)
	req := responsesRequest{Model: "m", Input: input}
	chat := convertToChatRequest(req, "")

	if len(chat.Messages) != 2 {
		t.Fatalf("messages: got %d, want 2", len(chat.Messages))
	}
	if chat.Messages[0]["role"] != "system" {
		t.Errorf("msg[0] role: got %q, want system", chat.Messages[0]["role"])
	}
	if chat.Messages[0]["content"] != "System prompt" {
		t.Errorf("msg[0] content: got %v", chat.Messages[0]["content"])
	}
	if chat.Messages[1]["role"] != "user" {
		t.Errorf("msg[1] role: got %q", chat.Messages[1]["role"])
	}
	if chat.Messages[1]["content"] != "hi" {
		t.Errorf("msg[1] content: got %v", chat.Messages[1]["content"])
	}
}

// ─── responsesRequest JSON 解析 ───

func TestResponsesRequest_UnmarshalStringInput(t *testing.T) {
	body := []byte(`{"model":"mimo","input":"hello","stream":true}`)
	var req responsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if req.Model != "mimo" {
		t.Errorf("model: got %q", req.Model)
	}
	if string(req.Input) != `"hello"` {
		t.Errorf("input: got %s", req.Input)
	}
	if !req.Stream {
		t.Error("expected stream=true")
	}
}

func TestResponsesRequest_UnmarshalArrayInput(t *testing.T) {
	body := []byte(`{"model":"mimo","input":[{"role":"user","content":"hi"}]}`)
	var req responsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if string(req.Input) != `[{"role":"user","content":"hi"}]` {
		t.Errorf("input: got %s", req.Input)
	}
}

func TestResponsesRequest_UnmarshalOptionalFields(t *testing.T) {
	body := []byte(`{
		"model":"mimo",
		"instructions":"Be helpful",
		"temperature":0.5,
		"max_output_tokens":2048,
		"tools":[{"type":"function"}]
	}`)
	var req responsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if req.Instructions != "Be helpful" {
		t.Errorf("instructions: got %q", req.Instructions)
	}
	if req.Temperature == nil || *req.Temperature != 0.5 {
		t.Errorf("temperature: got %v", req.Temperature)
	}
	if req.MaxTokens != 2048 {
		t.Errorf("max_output_tokens: got %d", req.MaxTokens)
	}
	if string(req.Tools) != `[{"type":"function"}]` {
		t.Errorf("tools: got %s", req.Tools)
	}
}
