// Package server provides the HTTP server, routing, security middleware, and CORS configuration.
package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"runtime"
	"time"
)

// readyzResponse models the payload returned by the readiness probe.
type readyzResponse struct {
	Status   string `json:"status"`
	Postgres string `json:"postgres"`
	Redis    string `json:"redis"`
}

// metricsResponse models the operational metrics JSON payload.
type metricsResponse struct {
	UptimeSeconds     uint64  `json:"uptime_seconds"`
	Goroutines        int     `json:"goroutines"`
	MemoryAllocBytes  uint64  `json:"memory_alloc_bytes"`
	MemorySysBytes    uint64  `json:"memory_sys_bytes"`
	PostgresStatus    string  `json:"postgres_status"`
	PostgresLatencyMs float64 `json:"postgres_latency_ms"`
	RedisStatus       string  `json:"redis_status"`
	RedisLatencyMs    float64 `json:"redis_latency_ms"`
	RequestCountTotal uint64  `json:"request_count_total"`
	ErrorCountTotal   uint64  `json:"error_count_total"`
	Timestamp         string  `json:"timestamp"`
}

// versionResponse models the application build metadata.
type versionResponse struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	GoVersion string `json:"go_version"`
}

// handleReadyz verifies that PostgreSQL and Redis dependencies are actively reachable.
// Uses a 5-second timeout per dependency to accommodate cross-region network roundtrips.
func (s *HTTPServer) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	pgStatus := "ok"
	redisStatus := "ok"

	// Validate PostgreSQL connectivity
	if s.db == nil {
		pgStatus = "unreachable"
	} else if err := s.db.PingContext(ctx); err != nil {
		log.Printf("[Readiness Probe] PostgreSQL ping failed: %v", err)
		pgStatus = "unreachable"
	}

	// Validate Redis connectivity
	if s.redisClient == nil {
		redisStatus = "unreachable"
	} else if err := s.redisClient.Ping(ctx).Err(); err != nil {
		log.Printf("[Readiness Probe] Redis ping failed: %v", err)
		redisStatus = "unreachable"
	}

	w.Header().Set("Content-Type", "application/json")

	if pgStatus != "ok" || redisStatus != "ok" {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(readyzResponse{
			Status:   "unhealthy",
			Postgres: pgStatus,
			Redis:    redisStatus,
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(readyzResponse{
		Status:   "ready",
		Postgres: "ok",
		Redis:    "ok",
	})
}

// handleMetrics returns instantaneous process metrics and live dependency ping latencies as JSON.
// Contains zero credentials, tokens, or sensitive network topologies.
func (s *HTTPServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	// Measure PostgreSQL live ping latency
	pgStatus := "connected"
	var pgLatencyMs float64
	if s.db == nil {
		pgStatus = "unreachable"
	} else {
		pgStart := time.Now()
		if err := s.db.PingContext(ctx); err != nil {
			log.Printf("[Metrics] PostgreSQL ping failed: %v", err)
			pgStatus = "unreachable"
		} else {
			pgLatencyMs = float64(time.Since(pgStart).Microseconds()) / 1000.0
		}
	}

	// Measure Redis live ping latency
	redisStatus := "connected"
	var redisLatencyMs float64
	if s.redisClient == nil {
		redisStatus = "unreachable"
	} else {
		rStart := time.Now()
		if err := s.redisClient.Ping(ctx).Err(); err != nil {
			log.Printf("[Metrics] Redis ping failed: %v", err)
			redisStatus = "unreachable"
		} else {
			redisLatencyMs = float64(time.Since(rStart).Microseconds()) / 1000.0
		}
	}

	uptime := uint64(time.Since(s.bootTime).Seconds())

	payload := metricsResponse{
		UptimeSeconds:     uptime,
		Goroutines:        runtime.NumGoroutine(),
		MemoryAllocBytes:  memStats.Alloc,
		MemorySysBytes:    memStats.Sys,
		PostgresStatus:    pgStatus,
		PostgresLatencyMs: pgLatencyMs,
		RedisStatus:       redisStatus,
		RedisLatencyMs:    redisLatencyMs,
		RequestCountTotal: s.requestCount.Load(),
		ErrorCountTotal:   s.errorCount.Load(),
		Timestamp:         time.Now().UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(payload)
}

// handleVersion returns the build version, commit SHA, and Go runtime compiler version.
func (s *HTTPServer) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(versionResponse{
		Version:   Version,
		Commit:    Commit,
		GoVersion: runtime.Version(),
	})
}
