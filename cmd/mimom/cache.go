package main

import (
	"container/list"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	defaultTTL      = 180 * time.Minute // 每条缓存 3 小时过期
	defaultMaxBytes = 64 * 1024 * 1024  // 总内存上限 64MB
	evictInterval   = 5 * time.Minute   // 定期清理周期
)

type cacheEntry struct {
	key       string
	value     string
	size      int
	expiresAt time.Time
}

// ReasoningStore 带 TTL 和容量上限的 reasoning_content 缓存。
type ReasoningStore struct {
	mu        sync.RWMutex
	entries   map[string]*list.Element
	order     *list.List // LRU 次序，front = 最近使用
	totalSize int
	maxBytes  int
	ttl       time.Duration
	stopCh    chan struct{}
}

func NewReasoningStore() *ReasoningStore {
	s := &ReasoningStore{
		entries:  make(map[string]*list.Element),
		order:    list.New(),
		maxBytes: defaultMaxBytes,
		ttl:      defaultTTL,
		stopCh:   make(chan struct{}),
	}
	go s.evictLoop()
	return s
}

func (s *ReasoningStore) Get(msgHash string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	elem, ok := s.entries[msgHash]
	if !ok {
		return "", false
	}
	entry := elem.Value.(*cacheEntry)
	if time.Now().After(entry.expiresAt) {
		s.removeElement(elem)
		return "", false
	}
	s.order.MoveToFront(elem)
	return entry.value, true
}

func (s *ReasoningStore) Set(msgHash, reasoning string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 已存在则更新
	if elem, ok := s.entries[msgHash]; ok {
		old := elem.Value.(*cacheEntry)
		s.totalSize -= old.size
		old.value = reasoning
		old.size = len(msgHash) + len(reasoning)
		old.expiresAt = time.Now().Add(s.ttl)
		s.totalSize += old.size
		s.order.MoveToFront(elem)
		return
	}

	size := len(msgHash) + len(reasoning)
	// 容量不足时淘汰
	for s.totalSize+size > s.maxBytes && s.order.Len() > 0 {
		s.evictOldest()
	}

	entry := &cacheEntry{
		key:       msgHash,
		value:     reasoning,
		size:      size,
		expiresAt: time.Now().Add(s.ttl),
	}
	elem := s.order.PushFront(entry)
	s.entries[msgHash] = elem
	s.totalSize += size
}

func (s *ReasoningStore) removeElement(elem *list.Element) {
	entry := elem.Value.(*cacheEntry)
	delete(s.entries, entry.key)
	s.order.Remove(elem)
	s.totalSize -= entry.size
}

func (s *ReasoningStore) evictOldest() {
	elem := s.order.Back()
	if elem != nil {
		s.removeElement(elem)
	}
}

// evictLoop 定期清理过期条目。
func (s *ReasoningStore) evictLoop() {
	ticker := time.NewTicker(evictInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.mu.Lock()
			now := time.Now()
			for e := s.order.Back(); e != nil; {
				prev := e.Prev()
				if now.After(e.Value.(*cacheEntry).expiresAt) {
					s.removeElement(e)
				}
				e = prev
			}
			s.mu.Unlock()
		case <-s.stopCh:
			return
		}
	}
}

func (s *ReasoningStore) Close() {
	close(s.stopCh)
}

// HashAssistantMsg 对 assistant 消息计算稳定哈希。
// 排除 OpenAI 的 reasoning_content 字段和 Anthropic 的 thinking content blocks。
func HashAssistantMsg(msg map[string]json.RawMessage) string {
	stable := make(map[string]json.RawMessage)
	for k, v := range msg {
		if k == "reasoning_content" {
			continue
		}
		// Anthropic: 从 content 数组中移除 thinking blocks
		if k == "content" {
			stable[k] = stripThinkingBlocks(v)
			continue
		}
		stable[k] = v
	}
	data, _ := json.Marshal(stable)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// stripThinkingBlocks 从 Anthropic content 数组中移除 thinking 类型的 block。
// 始终重新序列化以保证 key 顺序一致，确保哈希稳定。
func stripThinkingBlocks(content json.RawMessage) json.RawMessage {
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(content, &blocks); err != nil {
		return content
	}
	var filtered []map[string]json.RawMessage
	for _, b := range blocks {
		if string(b["type"]) == `"thinking"` {
			continue
		}
		filtered = append(filtered, b)
	}
	out, _ := json.Marshal(filtered)
	return out
}

// InjectReasoning 扫描请求中的 messages，为缺少推理内容的 assistant 消息注入缓存。
// 同时支持 OpenAI 的 reasoning_content 字段和 Anthropic 的 thinking content blocks。
func (s *ReasoningStore) InjectReasoning(body []byte) ([]byte, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body, nil
	}
	msgsRaw, ok := raw["messages"]
	if !ok {
		return body, nil
	}
	var msgs []map[string]json.RawMessage
	if err := json.Unmarshal(msgsRaw, &msgs); err != nil {
		return body, nil
	}

	modified := false
	for _, msg := range msgs {
		if string(msg["role"]) != `"assistant"` {
			continue
		}
		// 已有 reasoning_content（OpenAI），跳过
		if _, has := msg["reasoning_content"]; has {
			continue
		}
		// 已有 thinking blocks（Anthropic），跳过
		if hasThinkingBlocks(msg["content"]) {
			continue
		}

		hash := HashAssistantMsg(msg)
		rc, found := s.Get(hash)

		// Anthropic 格式：content 是数组 → 注入 thinking block
		if isContentArray(msg["content"]) {
			if found {
				injectThinkingBlock(msg, rc)
			} else {
				// 无缓存时注入空 thinking，防止上游 400
				injectThinkingBlock(msg, "")
			}
		} else {
			// OpenAI 格式：设置 reasoning_content 字段
			if !found {
				rc = ""
			}
			rcBytes, _ := json.Marshal(rc)
			msg["reasoning_content"] = rcBytes
		}
		modified = true
	}
	if !modified {
		return body, nil
	}
	raw["messages"], _ = json.Marshal(msgs)
	return json.Marshal(raw)
}

// isContentArray 判断 content 是否为数组格式（Anthropic）。
func isContentArray(content json.RawMessage) bool {
	if content == nil {
		return false
	}
	return len(content) > 0 && content[0] == '['
}

// hasThinkingBlocks 判断 content 数组中是否已有 thinking blocks。
func hasThinkingBlocks(content json.RawMessage) bool {
	if !isContentArray(content) {
		return false
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(content, &blocks); err != nil {
		return false
	}
	for _, b := range blocks {
		if string(b["type"]) == `"thinking"` {
			return true
		}
	}
	return false
}

// injectThinkingBlock 在 Anthropic 消息的 content 数组开头注入 thinking block。
func injectThinkingBlock(msg map[string]json.RawMessage, thinking string) {
	var blocks []map[string]json.RawMessage
	json.Unmarshal(msg["content"], &blocks)
	thinkingBlock := map[string]json.RawMessage{
		"type":     []byte(`"thinking"`),
		"thinking": mustMarshal(thinking),
	}
	newBlocks := append([]map[string]json.RawMessage{thinkingBlock}, blocks...)
	msg["content"], _ = json.Marshal(newBlocks)
}

func mustMarshal(v string) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// ExtractReasoning 从非流式响应中提取 assistant 消息的 reasoning_content 并缓存。
func (s *ReasoningStore) ExtractReasoning(respBody []byte) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return
	}
	choicesRaw, ok := raw["choices"]
	if !ok {
		return
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(choicesRaw, &choices); err != nil {
		return
	}
	for _, choice := range choices {
		msgRaw, ok := choice["message"]
		if !ok {
			continue
		}
		var msg map[string]json.RawMessage
		if err := json.Unmarshal(msgRaw, &msg); err != nil {
			continue
		}
		rcRaw, hasRC := msg["reasoning_content"]
		if !hasRC || string(rcRaw) == "null" || string(rcRaw) == `""` {
			continue
		}
		hash := HashAssistantMsg(msg)
		var rc string
		json.Unmarshal(rcRaw, &rc)
		s.Set(hash, rc)
	}
}

// CacheStreamReasoning 缓存流式收集到的 reasoning/thinking 内容。
func (s *ReasoningStore) CacheStreamReasoning(hashKey, reasoning string) {
	if hashKey != "" && reasoning != "" {
		s.Set(hashKey, reasoning)
	}
}

// ExtractAnthropicThinking 从 Anthropic 非流式响应中提取 thinking blocks 并缓存。
func (s *ReasoningStore) ExtractAnthropicThinking(respBody []byte) {
	var raw struct {
		Content []struct {
			Type     string `json:"type"`
			Thinking string `json:"thinking"`
		} `json:"content"`
		Role string `json:"role"`
	}
	if err := json.Unmarshal(respBody, &raw); err != nil {
		return
	}
	var buf strings.Builder
	for _, block := range raw.Content {
		if block.Type == "thinking" && block.Thinking != "" {
			buf.WriteString(block.Thinking)
		}
	}
	thinking := buf.String()
	if thinking == "" {
		return
	}
	// 用完整响应的 content（去掉 thinking）作为缓存 key
	hash := hashAnthropicResponse(raw.Content)
	s.Set(hash, thinking)
}

// hashAnthropicResponse 对 Anthropic 响应 content（不含 thinking）计算哈希。
func hashAnthropicResponse(content []struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking"`
}) string {
	var filtered []map[string]string
	for _, b := range content {
		if b.Type == "thinking" {
			continue
		}
		filtered = append(filtered, map[string]string{"type": b.Type, "text": b.Thinking})
	}
	data, _ := json.Marshal(filtered)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
