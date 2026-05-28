package main

import (
	"encoding/json"
	"testing"
)

func TestHashAssistantMsg_StableWithoutReasoning(t *testing.T) {
	msg := map[string]json.RawMessage{
		"role":              []byte(`"assistant"`),
		"content":           []byte(`"hello"`),
		"reasoning_content": []byte(`"thinking..."`),
	}
	h1 := HashAssistantMsg(msg)

	// 去掉 reasoning_content 后哈希应一致
	delete(msg, "reasoning_content")
	h2 := HashAssistantMsg(msg)

	if h1 != h2 {
		t.Errorf("hash should be stable regardless of reasoning_content: %s != %s", h1, h2)
	}
}

func TestHashAssistantMsg_StripThinkingBlocks(t *testing.T) {
	msg := map[string]json.RawMessage{
		"role": []byte(`"assistant"`),
		"content": []byte(`[
			{"type":"thinking","thinking":"reasoning..."},
			{"type":"text","text":"answer"}
		]`),
	}
	h1 := HashAssistantMsg(msg)

	// 去掉 thinking block 后哈希应一致
	msg2 := map[string]json.RawMessage{
		"role":    []byte(`"assistant"`),
		"content": []byte(`[{"type":"text","text":"answer"}]`),
	}
	h2 := HashAssistantMsg(msg2)

	if h1 != h2 {
		t.Errorf("hash should strip thinking blocks: %s != %s", h1, h2)
	}
}

func TestReasoningStore_SetGet(t *testing.T) {
	s := NewReasoningStore()
	defer s.Close()

	s.Set("key1", "reasoning text")
	v, ok := s.Get("key1")
	if !ok || v != "reasoning text" {
		t.Errorf("expected 'reasoning text', got %q (ok=%v)", v, ok)
	}
}

func TestReasoningStore_GetMiss(t *testing.T) {
	s := NewReasoningStore()
	defer s.Close()

	_, ok := s.Get("nonexistent")
	if ok {
		t.Error("expected miss for nonexistent key")
	}
}

func TestReasoningStore_LRUEviction(t *testing.T) {
	s := NewReasoningStore()
	defer s.Close()
	s.maxBytes = 5 // "a"+"x"=2, "b"+"yy"=3, 总计5已满

	s.Set("a", "x")
	s.Set("b", "yy")
	s.Set("c", "zzz") // "c"+"zzz"=4, 需要淘汰 "a"

	_, okA := s.Get("a")
	if okA {
		t.Error("expected 'a' to be evicted")
	}
	_, okC := s.Get("c")
	if !okC {
		t.Error("expected 'c' to exist")
	}
}

func TestInjectReasoning_OpenAI(t *testing.T) {
	s := NewReasoningStore()
	defer s.Close()

	// 预填缓存
	msg := map[string]json.RawMessage{
		"role":    []byte(`"assistant"`),
		"content": []byte(`"answer"`),
	}
	hash := HashAssistantMsg(msg)
	s.Set(hash, "cached reasoning")

	// 构造缺少 reasoning_content 的请求
	body := []byte(`{
		"model": "test",
		"messages": [
			{"role":"user","content":"hi"},
			{"role":"assistant","content":"answer"}
		]
	}`)

	patched, err := s.InjectReasoning(body)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]json.RawMessage
	json.Unmarshal(patched, &result)
	var msgs []map[string]json.RawMessage
	json.Unmarshal(result["messages"], &msgs)

	assistant := msgs[1]
	rc := string(assistant["reasoning_content"])
	if rc != `"cached reasoning"` {
		t.Errorf("expected reasoning_content to be injected, got %s", rc)
	}
}

func TestInjectReasoning_AnthropicThinking(t *testing.T) {
	s := NewReasoningStore()
	defer s.Close()

	// 预填缓存
	msg := map[string]json.RawMessage{
		"role":    []byte(`"assistant"`),
		"content": []byte(`[{"type":"text","text":"answer"}]`),
	}
	hash := HashAssistantMsg(msg)
	s.Set(hash, "cached thinking")

	// 构造缺少 thinking block 的请求
	body := []byte(`{
		"model": "test",
		"messages": [
			{"role":"user","content":"hi"},
			{"role":"assistant","content":[{"type":"text","text":"answer"}]}
		]
	}`)

	patched, err := s.InjectReasoning(body)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]json.RawMessage
	json.Unmarshal(patched, &result)
	var msgs []map[string]json.RawMessage
	json.Unmarshal(result["messages"], &msgs)

	// 检查 content 数组是否包含 thinking block
	var blocks []map[string]json.RawMessage
	json.Unmarshal(msgs[1]["content"], &blocks)

	found := false
	for _, b := range blocks {
		if string(b["type"]) == `"thinking"` {
			found = true
			if string(b["thinking"]) != `"cached thinking"` {
				t.Errorf("wrong thinking text: %s", string(b["thinking"]))
			}
		}
	}
	if !found {
		t.Error("expected thinking block to be injected")
	}
}

func TestInjectReasoning_SkipExisting(t *testing.T) {
	s := NewReasoningStore()
	defer s.Close()

	// 已有 reasoning_content 的消息不应被修改
	body := []byte(`{
		"model": "test",
		"messages": [
			{"role":"assistant","content":"a","reasoning_content":"existing"}
		]
	}`)

	patched, err := s.InjectReasoning(body)
	if err != nil {
		t.Fatal(err)
	}

	// 应原样返回（未修改）
	if string(patched) != string(body) {
		t.Error("expected body unchanged when reasoning_content already exists")
	}
}

func TestExtractReasoning(t *testing.T) {
	s := NewReasoningStore()
	defer s.Close()

	respBody := []byte(`{
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "answer",
				"reasoning_content": "the reasoning"
			}
		}]
	}`)

	s.ExtractReasoning(respBody)

	// 用同样的消息哈希查找
	msg := map[string]json.RawMessage{
		"role":    []byte(`"assistant"`),
		"content": []byte(`"answer"`),
	}
	hash := HashAssistantMsg(msg)
	v, ok := s.Get(hash)
	if !ok {
		t.Fatal("expected reasoning to be cached")
	}
	if v != "the reasoning" {
		t.Errorf("expected 'the reasoning', got %q", v)
	}
}

func TestExtractAnthropicThinking(t *testing.T) {
	s := NewReasoningStore()
	defer s.Close()

	respBody := []byte(`{
		"content": [
			{"type": "thinking", "thinking": "deep thought"},
			{"type": "text", "text": "answer"}
		]
	}`)

	s.ExtractAnthropicThinking(respBody)

	// 验证缓存中有数据（通过 hashAnthropicResponse 的 key）
	s.mu.RLock()
	count := len(s.entries)
	s.mu.RUnlock()
	if count == 0 {
		t.Error("expected thinking to be cached")
	}
}

func TestIsContentArray(t *testing.T) {
	if !isContentArray([]byte(`[{"type":"text"}]`)) {
		t.Error("expected true for array")
	}
	if isContentArray([]byte(`"string"`)) {
		t.Error("expected false for string")
	}
	if isContentArray(nil) {
		t.Error("expected false for nil")
	}
}

func TestHasThinkingBlocks(t *testing.T) {
	content := []byte(`[{"type":"thinking","thinking":"..."},{"type":"text","text":"hi"}]`)
	if !hasThinkingBlocks(content) {
		t.Error("expected true")
	}

	content2 := []byte(`[{"type":"text","text":"hi"}]`)
	if hasThinkingBlocks(content2) {
		t.Error("expected false")
	}
}

func TestInjectReasoning_OpenAI_EmptyFill(t *testing.T) {
	s := NewReasoningStore()
	defer s.Close()

	// 不预填缓存 — 应注入空 reasoning_content
	body := []byte(`{
		"model": "test",
		"messages": [
			{"role":"user","content":"hi"},
			{"role":"assistant","content":"answer"}
		]
	}`)

	patched, err := s.InjectReasoning(body)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]json.RawMessage
	json.Unmarshal(patched, &result)
	var msgs []map[string]json.RawMessage
	json.Unmarshal(result["messages"], &msgs)

	assistant := msgs[1]
	rc := string(assistant["reasoning_content"])
	if rc != `""` {
		t.Errorf("expected empty reasoning_content, got %s", rc)
	}
}

func TestInjectReasoning_Anthropic_EmptyFill(t *testing.T) {
	s := NewReasoningStore()
	defer s.Close()

	// 不预填缓存 — 应注入空 thinking block
	body := []byte(`{
		"model": "test",
		"messages": [
			{"role":"user","content":"hi"},
			{"role":"assistant","content":[{"type":"text","text":"answer"}]}
		]
	}`)

	patched, err := s.InjectReasoning(body)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]json.RawMessage
	json.Unmarshal(patched, &result)
	var msgs []map[string]json.RawMessage
	json.Unmarshal(result["messages"], &msgs)

	var blocks []map[string]json.RawMessage
	json.Unmarshal(msgs[1]["content"], &blocks)

	found := false
	for _, b := range blocks {
		if string(b["type"]) == `"thinking"` {
			found = true
			if string(b["thinking"]) != `""` {
				t.Errorf("expected empty thinking, got %s", string(b["thinking"]))
			}
		}
	}
	if !found {
		t.Error("expected empty thinking block to be injected")
	}
}
