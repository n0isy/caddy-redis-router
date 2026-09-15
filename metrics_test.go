package redisrouter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

func lookupCount(t *testing.T, reg *prometheus.Registry, outcome string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "caddy_redis_router_lookups_total" {
			continue
		}
		for _, metric := range family.Metric {
			if len(metric.Label) == 1 && metric.Label[0].GetValue() == outcome {
				return metric.GetCounter().GetValue()
			}
		}
	}
	t.Fatalf("missing outcome %s", outcome)
	return 0
}

func TestLookupMetricsAgainstRedis(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDRESS")
	if addr == "" {
		t.Skip("set TEST_REDIS_ADDRESS to an isolated Redis for middleware integration")
	}
	client := redis.NewClient(&redis.Options{Addr: addr, Protocol: 2, DisableIdentity: true})
	defer client.Close()
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	metrics, err := newRouterMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := newRouterMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	if shared.lookups != metrics.lookups {
		t.Fatal("reload did not reuse registry counter")
	}
	prefix := "router-test:" + time.Now().Format("150405.000000000") + ":"
	m := &RedisRouter{client: client, metrics: metrics, KeyPrefix: prefix, Key: "{http.request.host}", Var: "upstream", OnMiss: 503, Timeout: caddy.Duration(time.Second)}
	cases := []struct {
		name, value, outcome string
		miss, cancel, pass   bool
		nextError            bool
	}{
		{name: "json", value: `{"upstream":"127.0.0.1:5173","headers":{"X-Internal-Auth":"fixture"}}`, outcome: "hit"},
		{name: "bare", value: "127.0.0.1:5173", outcome: "hit"},
		{name: "empty", value: `{"upstream":""}`, outcome: "invalid_route"},
		{name: "missing", miss: true, outcome: "miss"},
		{name: "passthrough", miss: true, pass: true, outcome: "miss"},
		{name: "cancelled", cancel: true, outcome: "redis_error"},
		{name: "upstream-error", value: "127.0.0.1:5173", outcome: "hit", nextError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := prefix + tc.name
			if !tc.miss {
				if err := client.Set(context.Background(), key, tc.value, time.Minute).Err(); err != nil {
					t.Fatal(err)
				}
			}
			defer client.Del(context.Background(), key)
			req := httptest.NewRequest(http.MethodGet, "http://"+tc.name+"/", nil)
			repl := caddy.NewReplacer()
			repl.Set("http.request.host", tc.name)
			ctx := context.WithValue(req.Context(), caddy.ReplacerCtxKey, repl)
			ctx = context.WithValue(ctx, caddyhttp.VarsCtxKey, map[string]any{})
			if tc.cancel {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelCtx
			}
			req = req.WithContext(ctx)
			m.OnMiss = 503
			if tc.pass {
				m.OnMiss = -1
			}
			before := lookupCount(t, reg, tc.outcome)
			called := false
			err := m.ServeHTTP(httptest.NewRecorder(), req, caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
				called = true
				if !tc.pass && caddyhttp.GetVar(r.Context(), "upstream") != "127.0.0.1:5173" {
					t.Fatal("route not passed to downstream")
				}
				if tc.nextError {
					return errors.New("upstream failed")
				}
				return nil
			}))
			if called != (tc.outcome == "hit" || tc.pass) {
				t.Fatalf("next called=%v", called)
			}
			wantError := (tc.outcome != "hit" && !tc.pass) || tc.nextError
			if (err != nil) != wantError {
				t.Fatalf("error=%v", err)
			}
			if got := lookupCount(t, reg, tc.outcome); got != before+1 {
				t.Fatalf("lookup count=%v want %v", got, before+1)
			}
		})
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == "caddy_redis_router_lookups_inflight" && f.Metric[0].GetGauge().GetValue() != 0 {
			t.Fatal("inflight leaked")
		}
	}
}
