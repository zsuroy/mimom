package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"time"
)

//go:embed dashboard.html
var dashboardHTML string

type DashboardHandler struct {
	cfg    *Config
	stats  *Stats
	reason *ReasoningStore
}

func NewDashboardHandler(cfg *Config, stats *Stats, reason *ReasoningStore) *DashboardHandler {
	return &DashboardHandler{cfg: cfg, stats: stats, reason: reason}
}

func (h *DashboardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/dashboard/api" {
		h.serveAPI(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(dashboardHTML))
}

func (h *DashboardHandler) serveAPI(w http.ResponseWriter, _ *http.Request) {
	type backendInfo struct {
		Name   string   `json:"name"`
		URL    string   `json:"url"`
		Type   string   `json:"type"`
		Models []string `json:"models"`
	}

	backends := make([]backendInfo, 0, len(h.cfg.Backends))
	for name, b := range h.cfg.Backends {
		models := make([]string, 0, len(b.Models))
		for clientName := range b.Models {
			models = append(models, clientName)
		}
		backends = append(backends, backendInfo{
			Name:   name,
			URL:    b.BaseURL,
			Type:   b.Type,
			Models: models,
		})
	}

	h.reason.mu.RLock()
	cacheEntries := len(h.reason.entries)
	cacheSize := h.reason.totalSize
	h.reason.mu.RUnlock()

	s := h.stats
	resp := map[string]any{
		"uptime_ns": s.Uptime().Nanoseconds(),
		"requests": map[string]any{
			"total":      s.total.Load(),
			"success":    s.success.Load(),
			"client_err": s.clientErr.Load(),
			"server_err": s.serverErr.Load(),
			"rps":        s.RPS(),
		},
		"streaming": s.streamCount.Load(),
		"latency": map[string]int64{
			"avg_ms": s.AvgLatencyMs(),
			"max_ms": s.MaxLatencyMs(),
		},
		"cache": map[string]any{
			"hits":      s.cacheHits.Load(),
			"misses":    s.cacheMisses.Load(),
			"entries":   cacheEntries,
			"memory_kb": cacheSize / 1024,
		},
		"backends":    backends,
		"recent_logs": s.RecentLogs(),
		"time_series": s.TimeSeries(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type dashStatusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *dashStatusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func StatsMiddleware(stats *Stats, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &dashStatusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		stats.RecordRequest(r.Method, r.URL.Path, rec.status, time.Since(start), false)
	})
}
