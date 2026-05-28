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
	if chat.Messages[0].Role != "user" {
		t.Errorf("role: got %q, want %q", chat.Messages[0].Role, "user")
	}
	if chat.Messages[0].Content != "Hello, world!" {
		t.Errorf("content: got %q, want %q", chat.Messages[0].Content, "Hello, world!")
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
	if chat.Messages[0].Role != "user" || chat.Messages[0].Content != "hi" {
		t.Errorf("msg[0]: got role=%q content=%q", chat.Messages[0].Role, chat.Messages[0].Content)
	}
	if chat.Messages[1].Role != "assistant" || chat.Messages[1].Content != "hello" {
		t.Errorf("msg[1]: got role=%q content=%q", chat.Messages[1].Role, chat.Messages[1].Content)
	}
	if chat.Messages[2].Role != "user" || chat.Messages[2].Content != "how are you?" {
		t.Errorf("msg[2]: got role=%q content=%q", chat.Messages[2].Role, chat.Messages[2].Content)
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
	if chat.Messages[0].Role != "system" {
		t.Errorf("msg[0] role: got %q, want %q", chat.Messages[0].Role, "system")
	}
	if chat.Messages[0].Content != "You are a helpful assistant." {
		t.Errorf("msg[0] content: got %q", chat.Messages[0].Content)
	}
	if chat.Messages[1].Role != "user" {
		t.Errorf("msg[1] role: got %q, want %q", chat.Messages[1].Role, "user")
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
	if chat.Messages[0].Role != "system" {
		t.Errorf("role: got %q, want %q", chat.Messages[0].Role, "system")
	}
}

func TestConvertToChatRequest_MultimodalArrayInput(t *testing.T) {
	// 数组格式里 content 可以是复杂结构，但 convertToChatRequest 只取 string
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
	if chat.Messages[0].Role != "system" || chat.Messages[0].Content != "System prompt" {
		t.Errorf("msg[0]: got role=%q content=%q", chat.Messages[0].Role, chat.Messages[0].Content)
	}
	if chat.Messages[1].Role != "system" || chat.Messages[1].Content != "You are helpful." {
		t.Errorf("msg[1]: got role=%q content=%q", chat.Messages[1].Role, chat.Messages[1].Content)
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
