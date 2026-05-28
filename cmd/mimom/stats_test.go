package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStats_RecordRequest(t *testing.T) {
	s := NewStats()

	s.RecordRequest("POST", "/v1/chat/completions", 200, 100*time.Millisecond, false)
	s.RecordRequest("POST", "/v1/chat/completions", 200, 50*time.Millisecond, true)
	s.RecordRequest("POST", "/v1/responses", 400, 10*time.Millisecond, false)
	s.RecordRequest("GET", "/v1/models", 502, 200*time.Millisecond, false)

	if s.total.Load() != 4 {
		t.Errorf("total: got %d, want 4", s.total.Load())
	}
	if s.success.Load() != 2 {
		t.Errorf("success: got %d, want 2", s.success.Load())
	}
	if s.clientErr.Load() != 1 {
		t.Errorf("clientErr: got %d, want 1", s.clientErr.Load())
	}
	if s.serverErr.Load() != 1 {
		t.Errorf("serverErr: got %d, want 1", s.serverErr.Load())
	}
	if s.streamCount.Load() != 1 {
		t.Errorf("streamCount: got %d, want 1", s.streamCount.Load())
	}
}

func TestStats_RecentLogs(t *testing.T) {
	s := NewStats()

	s.RecordRequest("GET", "/health", 200, time.Millisecond, false)
	s.RecordRequest("POST", "/v1/chat", 200, time.Millisecond, false)

	logs := s.RecentLogs()
	if len(logs) != 2 {
		t.Fatalf("logs: got %d, want 2", len(logs))
	}
	if logs[0].Method != "GET" {
		t.Errorf("log[0] method: got %q", logs[0].Method)
	}
	if logs[1].Path != "/v1/chat" {
		t.Errorf("log[1] path: got %q", logs[1].Path)
	}
}

func TestStats_RecentLogs_MaxCap(t *testing.T) {
	s := NewStats()

	for i := 0; i < 120; i++ {
		s.RecordRequest("GET", "/test", 200, time.Millisecond, false)
	}

	logs := s.RecentLogs()
	if len(logs) != maxRecentLogs {
		t.Errorf("logs: got %d, want %d (max cap)", len(logs), maxRecentLogs)
	}
}

func TestStats_CacheCounters(t *testing.T) {
	s := NewStats()
	s.RecordCacheHit()
	s.RecordCacheHit()
	s.RecordCacheMiss()

	if s.cacheHits.Load() != 2 {
		t.Errorf("cacheHits: got %d, want 2", s.cacheHits.Load())
	}
	if s.cacheMisses.Load() != 1 {
		t.Errorf("cacheMisses: got %d, want 1", s.cacheMisses.Load())
	}
}

func TestStats_Uptime(t *testing.T) {
	s := NewStats()
	time.Sleep(10 * time.Millisecond)
	if s.Uptime() < 10*time.Millisecond {
		t.Error("uptime should be >= 10ms")
	}
}

func TestDashboardHandler_HTML(t *testing.T) {
	cfg := &Config{
		Backends: map[string]BackendDef{
			"test": {BaseURL: "http://localhost:1", Models: map[string]string{"m": "r"}},
		},
	}
	stats := NewStats()
	reason := NewReasoningStore()
	defer reason.Close()
	h := NewDashboardHandler(cfg, stats, reason)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status: got %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("content-type: got %q", ct)
	}
	body := w.Body.String()
	if len(body) == 0 {
		t.Error("expected non-empty HTML body")
	}
}

func TestDashboardHandler_API(t *testing.T) {
	cfg := &Config{
		Backends: map[string]BackendDef{
			"test": {BaseURL: "http://localhost:1", Models: map[string]string{"m": "r"}},
		},
	}
	stats := NewStats()
	stats.RecordRequest("GET", "/health", 200, time.Millisecond, false)
	reason := NewReasoningStore()
	defer reason.Close()
	h := NewDashboardHandler(cfg, stats, reason)

	req := httptest.NewRequest(http.MethodGet, "/dashboard/api", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Errorf("status: got %d, want 200", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)

	if resp["uptime_ns"] == nil {
		t.Error("missing uptime_ns")
	}
	reqs := resp["requests"].(map[string]any)
	if int(reqs["total"].(float64)) != 1 {
		t.Errorf("total: got %v", reqs["total"])
	}
	backends := resp["backends"].([]any)
	if len(backends) != 1 {
		t.Errorf("backends: got %d, want 1", len(backends))
	}
}

func TestStatsMiddleware(t *testing.T) {
	stats := NewStats()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	handler := StatsMiddleware(stats, inner)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if stats.total.Load() != 1 {
		t.Errorf("total: got %d, want 1", stats.total.Load())
	}
	if stats.success.Load() != 1 {
		t.Errorf("success: got %d, want 1", stats.success.Load())
	}
}
