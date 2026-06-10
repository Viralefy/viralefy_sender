// Package observability expõe os collectors Prometheus do viralefy_sender.
//
// Mantém os nomes de métricas idênticos ao viralefy_core
// (http_requests_total, http_request_duration_seconds) pra reaproveitar
// dashboards de Grafana sem mudanças — basta filtrar por label
// service=viralefy-sender no Prometheus.
package observability

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total de requests HTTP processados, com labels method, path, status.",
		},
		[]string{"method", "path", "status"},
	)

	HTTPRequestDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Duração das requests HTTP em segundos.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)

	DBQueryDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "db_query_duration_seconds",
			Help:    "Duração de queries SQL agrupadas por tipo lógico.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0},
		},
		[]string{"query_type"},
	)

	// OutboxDispatchTotal: contagem de mensagens despachadas pelo Tick por
	// canal e status (sent, retried, dead-lettered) — útil pra alertar
	// quando o outbox cresce sem drenar.
	OutboxDispatchTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sender_outbox_dispatch_total",
			Help: "Mensagens processadas pelo outbox tick, com labels channel e status.",
		},
		[]string{"channel", "status"},
	)
)

var (
	metricsRegistry *prometheus.Registry
	metricsOnce     sync.Once
)

// InitMetrics regista os collectors num Registry isolado. Idempotente.
func InitMetrics() *prometheus.Registry {
	metricsOnce.Do(func() {
		reg := prometheus.NewRegistry()
		reg.MustRegister(
			collectors.NewGoCollector(),
			collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
			HTTPRequestsTotal,
			HTTPRequestDurationSeconds,
			DBQueryDurationSeconds,
			OutboxDispatchTotal,
		)
		metricsRegistry = reg
	})
	return metricsRegistry
}

// MetricsHandler devolve o handler HTTP do /metrics. Use após InitMetrics.
func MetricsHandler() http.Handler {
	if metricsRegistry == nil {
		InitMetrics()
	}
	return promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
		Registry:          metricsRegistry,
	})
}

// HTTPMiddleware instrumenta cada request com http_requests_total +
// http_request_duration_seconds. Usa chi.RouteContext.RoutePattern() pra
// evitar explosão de cardinalidade.
func HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		defer func() {
			pathLabel := ""
			if rc := chi.RouteContext(r.Context()); rc != nil {
				pathLabel = rc.RoutePattern()
			}
			if pathLabel == "" {
				pathLabel = "unknown"
			}
			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			HTTPRequestsTotal.WithLabelValues(r.Method, pathLabel, strconv.Itoa(status)).Inc()
			HTTPRequestDurationSeconds.WithLabelValues(r.Method, pathLabel).Observe(time.Since(start).Seconds())
		}()

		next.ServeHTTP(ww, r)
	})
}

// ObserveDBQuery: shorthand para instrumentar uma query SQL.
//
//	defer observability.ObserveDBQuery("outbox_lease")(time.Now())
func ObserveDBQuery(queryType string) func(start time.Time) {
	return func(start time.Time) {
		DBQueryDurationSeconds.WithLabelValues(queryType).Observe(time.Since(start).Seconds())
	}
}
