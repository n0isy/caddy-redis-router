package redisrouter

import (
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Labels are fixed outcomes only. Request hosts, Redis keys and injected
// headers must never become metric labels or samples.
type routerMetrics struct {
	lookups  *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inflight prometheus.Gauge
}

func registerMetric[T prometheus.Collector](registry prometheus.Registerer, candidate T) (T, error) {
	if err := registry.Register(candidate); err != nil {
		var duplicate prometheus.AlreadyRegisteredError
		if errors.As(err, &duplicate) {
			if existing, ok := duplicate.ExistingCollector.(T); ok {
				return existing, nil
			}
		}
		return candidate, err
	}
	return candidate, nil
}

func newRouterMetrics(registry prometheus.Registerer) (*routerMetrics, error) {
	counts, err := registerMetric(registry, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "caddy_redis_router_lookups_total", Help: "Redis route lookups by outcome; no request identifiers."}, []string{"outcome"}))
	if err != nil {
		return nil, err
	}
	duration, err := registerMetric(registry, prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "caddy_redis_router_lookup_duration_seconds", Help: "Redis lookup and route decoding duration.", Buckets: []float64{.001, .005, .01, .05, .1, .5, 1, 2, 5}}, []string{"outcome"}))
	if err != nil {
		return nil, err
	}
	inflight, err := registerMetric(registry, prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "caddy_redis_router_lookups_inflight", Help: "Current Redis route lookups, excluding upstream request execution."}))
	if err != nil {
		return nil, err
	}
	for _, outcome := range []string{"hit", "miss", "redis_error", "invalid_route"} {
		counts.WithLabelValues(outcome)
		duration.WithLabelValues(outcome)
	}
	return &routerMetrics{counts, duration, inflight}, nil
}

func (m *routerMetrics) begin() func(string) {
	if m == nil {
		return func(string) {}
	}
	start := time.Now()
	m.inflight.Inc()
	return func(outcome string) {
		m.inflight.Dec()
		m.lookups.WithLabelValues(outcome).Inc()
		m.duration.WithLabelValues(outcome).Observe(time.Since(start).Seconds())
	}
}
