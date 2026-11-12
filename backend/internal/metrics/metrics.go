package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	RequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "llm_gateway_request_duration_seconds",
			Help:    "Time to complete upstream request",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 3, 5, 10},
		},
		[]string{"model", "status"},
	)

	TokensConsumed = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_tokens_total",
			Help: "Total tokens consumed",
		},
		[]string{"model"},
	)

	BalanceCheckDuration = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "llm_gateway_balance_check_duration_seconds",
			Help:    "Time to check and reserve balance",
			Buckets: prometheus.DefBuckets,
		},
	)

	InsufficientBalance = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "llm_gateway_insufficient_balance_total",
			Help: "Number of requests rejected due to insufficient balance",
		},
	)

	RateLimited = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "llm_gateway_rate_limited_total",
			Help: "Number of requests rejected by rate limiter",
		},
	)

	UpstreamErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llm_gateway_upstream_errors_total",
			Help: "Number of upstream errors",
		},
		[]string{"type"},
	)

	ActiveRequests = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "llm_gateway_active_requests",
			Help: "Number of requests currently in flight",
		},
	)
)
