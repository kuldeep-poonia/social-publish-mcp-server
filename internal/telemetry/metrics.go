// Package telemetry provides Prometheus metrics instrumentation, structured logging, and automated secret scrubbing.
package telemetry

import (
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// TelemetryRegistry encapsulates all Prometheus metric collectors for the Social MCP Server.
type TelemetryRegistry struct {
	Registry              *prometheus.Registry
	RequestsTotal         *prometheus.CounterVec
	RequestDuration       *prometheus.HistogramVec
	QueueDepth            *prometheus.GaugeVec
	QueueJobDuration      *prometheus.HistogramVec
	RateLimitBlocksTotal  *prometheus.CounterVec
	YouTubeQuotaUnitsUsed *prometheus.GaugeVec
	DeadLetterTotal       *prometheus.CounterVec
}

// Global default telemetry registry instance.
var defaultTelemetry *TelemetryRegistry

func init() {
	defaultTelemetry = NewTelemetryRegistry()
}

// DefaultTelemetry returns the singleton telemetry registry.
func DefaultTelemetry() *TelemetryRegistry {
	return defaultTelemetry
}

// NewTelemetryRegistry initializes and registers all application metrics.
func NewTelemetryRegistry() *TelemetryRegistry {
	reg := prometheus.NewRegistry()

	requestsTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "social_mcp_requests_total",
			Help: "Total incoming HTTP and MCP protocol requests processed by the server",
		},
		[]string{"method", "platform", "status_code", "client_id"},
	)

	requestDuration := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "social_mcp_request_duration_seconds",
			Help:    "Histogram of request processing duration in seconds",
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0},
		},
		[]string{"method", "platform", "status_code"},
	)

	queueDepth := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "social_mcp_queue_depth",
			Help: "Current count of queued publish jobs in Redis stream",
		},
		[]string{"stream_name", "status"},
	)

	queueJobDuration := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "social_mcp_queue_job_duration_seconds",
			Help:    "Histogram of background queue retry execution duration in seconds",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1.0, 2.0, 5.0, 10.0, 30.0},
		},
		[]string{"platform", "status"},
	)

	rateLimitBlocksTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "social_mcp_rate_limit_blocks_total",
			Help: "Total requests throttled with HTTP 429 by the distributed rate limiter",
		},
		[]string{"platform", "key_type"},
	)

	youtubeQuotaUnitsUsed := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "social_mcp_youtube_quota_units_used",
			Help: "Daily YouTube Data API v3 quota units consumed per tenant",
		},
		[]string{"tenant_id"},
	)

	deadLetterTotal := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "social_mcp_dead_letter_total",
			Help: "Total unrecoverable publish jobs diverted to dead-letter queue",
		},
		[]string{"platform", "error_category"},
	)

	reg.MustRegister(
		requestsTotal,
		requestDuration,
		queueDepth,
		queueJobDuration,
		rateLimitBlocksTotal,
		youtubeQuotaUnitsUsed,
		deadLetterTotal,
	)

	return &TelemetryRegistry{
		Registry:              reg,
		RequestsTotal:         requestsTotal,
		RequestDuration:       requestDuration,
		QueueDepth:            queueDepth,
		QueueJobDuration:      queueJobDuration,
		RateLimitBlocksTotal:  rateLimitBlocksTotal,
		YouTubeQuotaUnitsUsed: youtubeQuotaUnitsUsed,
		DeadLetterTotal:       deadLetterTotal,
	}
}

// ObserveRequest records request throughput and latency.
func (t *TelemetryRegistry) ObserveRequest(method, platform string, statusCode string, clientID string, duration time.Duration) {
	if platform == "" {
		platform = "none"
	}
	if clientID == "" {
		clientID = "anonymous"
	}
	t.RequestsTotal.WithLabelValues(method, platform, statusCode, clientID).Inc()
	t.RequestDuration.WithLabelValues(method, platform, statusCode).Observe(duration.Seconds())
}

// RecordRateLimitBlock increments the 429 throttled request counter.
func (t *TelemetryRegistry) RecordRateLimitBlock(platform, keyType string) {
	if platform == "" {
		platform = "global"
	}
	t.RateLimitBlocksTotal.WithLabelValues(platform, keyType).Inc()
}

// RecordQueueJob records background retry job execution metrics.
func (t *TelemetryRegistry) RecordQueueJob(platform, status string, duration time.Duration) {
	t.QueueJobDuration.WithLabelValues(platform, status).Observe(duration.Seconds())
}

// RecordDeadLetter increments the dead letter routing counter.
func (t *TelemetryRegistry) RecordDeadLetter(platform, errorCategory string) {
	t.DeadLetterTotal.WithLabelValues(platform, errorCategory).Inc()
}

// UpdateQueueDepth sets the current depth gauge for a stream.
func (t *TelemetryRegistry) UpdateQueueDepth(streamName, status string, count float64) {
	t.QueueDepth.WithLabelValues(streamName, status).Set(count)
}

// UpdateYouTubeQuota sets the consumed daily quota units for a tenant.
func (t *TelemetryRegistry) UpdateYouTubeQuota(tenantID string, units float64) {
	t.YouTubeQuotaUnitsUsed.WithLabelValues(tenantID).Set(units)
}

// MetricsHandler returns an HTTP handler for scraping Prometheus metrics, protected by Bearer token authentication.
func (t *TelemetryRegistry) MetricsHandler(metricsBearerToken string) http.Handler {
	promHandler := promhttp.HandlerFor(t.Registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strict Bearer Token Authentication
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Unauthorized: Authorization header required", http.StatusUnauthorized)
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			http.Error(w, "Unauthorized: Bearer token format required", http.StatusUnauthorized)
			return
		}

		token := strings.TrimSpace(parts[1])
		if metricsBearerToken == "" || token != metricsBearerToken {
			http.Error(w, "Forbidden: invalid metrics bearer token", http.StatusUnauthorized)
			return
		}

		promHandler.ServeHTTP(w, r)
	})
}
