package main

import (
	"sync"
	"sync/atomic"
	"time"
)

const maxRecentLogs = 100

type RequestLog struct {
	Time     time.Time `json:"time"`
	Method   string    `json:"method"`
	Path     string    `json:"path"`
	Status   int       `json:"status"`
	Duration string    `json:"duration"`
}

type Stats struct {
	startTime   time.Time
	total       atomic.Int64
	success     atomic.Int64 // 2xx
	clientErr   atomic.Int64 // 4xx
	serverErr   atomic.Int64 // 5xx
	streamCount atomic.Int64
	cacheHits   atomic.Int64
	cacheMisses atomic.Int64

	mu         sync.Mutex
	recentLogs []RequestLog

	latencySum   atomic.Int64 // 累计延迟（ms）
	latencyCount atomic.Int64 // 用于计算平均值
	latencyMax   atomic.Int64 // 最大延迟（ms）

	timeSeriesMu sync.Mutex
	timeSeries   []TimeSeriesPoint
}

type TimeSeriesPoint struct {
	Timestamp int64 `json:"t"` // unix seconds
	Count     int64 `json:"c"`
	Errors    int64 `json:"e"`
	AvgMs     int64 `json:"ms"`
}

const maxTimeSeriesPoints = 120

func NewStats() *Stats {
	return &Stats{
		startTime:  time.Now(),
		recentLogs: make([]RequestLog, 0, maxRecentLogs),
		timeSeries: make([]TimeSeriesPoint, 0, maxTimeSeriesPoints),
	}
}

func (s *Stats) RecordRequest(method, path string, status int, duration time.Duration, isStream bool) {
	s.total.Add(1)

	isErr := false
	switch {
	case status >= 200 && status < 300:
		s.success.Add(1)
	case status >= 400 && status < 500:
		s.clientErr.Add(1)
		isErr = true
	case status >= 500:
		s.serverErr.Add(1)
		isErr = true
	}

	if isStream {
		s.streamCount.Add(1)
	}

	ms := duration.Milliseconds()
	s.latencySum.Add(ms)
	s.latencyCount.Add(1)

	for {
		old := s.latencyMax.Load()
		if ms <= old {
			break
		}
		if s.latencyMax.CompareAndSwap(old, ms) {
			break
		}
	}

	log := RequestLog{
		Time:     time.Now(),
		Method:   method,
		Path:     path,
		Status:   status,
		Duration: duration.Round(time.Millisecond).String(),
	}

	s.mu.Lock()
	if len(s.recentLogs) >= maxRecentLogs {
		s.recentLogs = s.recentLogs[1:]
	}
	s.recentLogs = append(s.recentLogs, log)
	s.mu.Unlock()

	s.recordTimeSeries(isErr, ms)
}

func (s *Stats) recordTimeSeries(isErr bool, ms int64) {
	now := time.Now().Unix()

	s.timeSeriesMu.Lock()
	defer s.timeSeriesMu.Unlock()

	if len(s.timeSeries) > 0 {
		last := &s.timeSeries[len(s.timeSeries)-1]
		if last.Timestamp == now {
			last.Count++
			if isErr {
				last.Errors++
			}
			last.AvgMs = (last.AvgMs*(last.Count-1) + ms) / last.Count
			return
		}
	}

	s.timeSeries = append(s.timeSeries, TimeSeriesPoint{
		Timestamp: now,
		Count:     1,
		Errors:    boolToInt64(isErr),
		AvgMs:     ms,
	})

	if len(s.timeSeries) > maxTimeSeriesPoints {
		s.timeSeries = s.timeSeries[len(s.timeSeries)-maxTimeSeriesPoints:]
	}
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func (s *Stats) RecordCacheHit() {
	s.cacheHits.Add(1)
}

func (s *Stats) RecordCacheMiss() {
	s.cacheMisses.Add(1)
}

func (s *Stats) RecentLogs() []RequestLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]RequestLog, len(s.recentLogs))
	copy(cp, s.recentLogs)
	return cp
}

func (s *Stats) Uptime() time.Duration {
	return time.Since(s.startTime)
}

func (s *Stats) AvgLatencyMs() int64 {
	count := s.latencyCount.Load()
	if count == 0 {
		return 0
	}
	return s.latencySum.Load() / count
}

func (s *Stats) MaxLatencyMs() int64 {
	return s.latencyMax.Load()
}

func (s *Stats) TimeSeries() []TimeSeriesPoint {
	s.timeSeriesMu.Lock()
	defer s.timeSeriesMu.Unlock()
	cp := make([]TimeSeriesPoint, len(s.timeSeries))
	copy(cp, s.timeSeries)
	return cp
}

func (s *Stats) RPS() float64 {
	elapsed := time.Since(s.startTime).Seconds()
	if elapsed < 1 {
		return 0
	}
	return float64(s.total.Load()) / elapsed
}
