// Package redisrouter is a Caddy HTTP handler that resolves a request's upstream
// (and optional per-route headers) by looking the request up in Redis.
//
// It turns a static Caddyfile into a data-driven router: the route table lives
// in Redis and can change at runtime with zero Caddy reloads. For each request
// the handler GETs `<key_prefix><key>` (key defaults to the request Host),
// expects a small JSON document, stashes the upstream address in a Caddy
// variable for `reverse_proxy` to consume, and copies any headers onto the
// request before handing off.
//
// The Redis value is JSON:
//
//	{
//	  "upstream": "10.1.2.3:8080",
//	  "headers":  { "X-Internal-Auth": "s3cr3t" }
//	}
//
// A bare "host:port" string is also accepted (no headers).
//
// Example Caddyfile:
//
//	{
//	    order redis_router before reverse_proxy
//	}
//
//	*.example.com {
//	    redis_router {
//	        address    127.0.0.1:6379
//	        key_prefix "route:"
//	        key        {http.request.host}
//	    }
//	    reverse_proxy {http.vars.upstream}
//	}
package redisrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func init() {
	caddy.RegisterModule(RedisRouter{})
	httpcaddyfile.RegisterHandlerDirective("redis_router", parseCaddyfile)
}

// RedisRouter resolves the upstream and per-route headers for a request from
// Redis. It is an http.handlers middleware: it sets a variable and (optionally)
// request headers, then calls the next handler (typically reverse_proxy).
type RedisRouter struct {
	// Address of the Redis server, "host:port". Default "127.0.0.1:6379".
	Address string `json:"address,omitempty"`
	// Password for Redis AUTH. Supports placeholders, e.g. "{env.REDIS_PASSWORD}".
	Password string `json:"password,omitempty"`
	// DB is the Redis database index. Default 0.
	DB int `json:"db,omitempty"`
	// KeyPrefix is prepended to the looked-up key. Default "route:".
	KeyPrefix string `json:"key_prefix,omitempty"`
	// Key is the placeholder-expanded lookup key. Default "{http.request.host}".
	Key string `json:"key,omitempty"`
	// Var is the Caddy variable the upstream is stored in; read it back as
	// {http.vars.<Var>}. Default "upstream".
	Var string `json:"var,omitempty"`
	// OnMiss is the HTTP status returned when the key is absent. Default 503.
	// Set to -1 to fall through to the next handler instead of erroring.
	OnMiss int `json:"on_miss,omitempty"`
	// Timeout bounds each Redis lookup. Default 2s.
	Timeout caddy.Duration `json:"timeout,omitempty"`

	client *redis.Client
	logger *zap.Logger
}

// route is the JSON document stored at each Redis key.
type route struct {
	Upstream string            `json:"upstream"`
	Headers  map[string]string `json:"headers,omitempty"`
}

// CaddyModule returns the Caddy module information.
func (RedisRouter) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.redis_router",
		New: func() caddy.Module { return new(RedisRouter) },
	}
}

// Provision applies defaults and opens the Redis client, failing fast if the
// server is unreachable.
func (m *RedisRouter) Provision(ctx caddy.Context) error {
	m.logger = ctx.Logger()
	repl := caddy.NewReplacer()

	if m.Address == "" {
		m.Address = "127.0.0.1:6379"
	}
	if m.KeyPrefix == "" {
		m.KeyPrefix = "route:"
	}
	if m.Key == "" {
		m.Key = "{http.request.host}"
	}
	if m.Var == "" {
		m.Var = "upstream"
	}
	if m.OnMiss == 0 {
		m.OnMiss = http.StatusServiceUnavailable // 503
	}
	if m.Timeout == 0 {
		m.Timeout = caddy.Duration(2 * time.Second)
	}

	m.client = redis.NewClient(&redis.Options{
		Addr:     repl.ReplaceAll(m.Address, ""),
		Password: repl.ReplaceAll(m.Password, ""),
		DB:       m.DB,
	})

	pingCtx, cancel := context.WithTimeout(ctx, time.Duration(m.Timeout))
	defer cancel()
	if err := m.client.Ping(pingCtx).Err(); err != nil {
		return fmt.Errorf("redis_router: cannot reach Redis at %s: %w", m.Address, err)
	}

	m.logger.Info("redis_router provisioned",
		zap.String("address", m.Address),
		zap.Int("db", m.DB),
		zap.String("key_prefix", m.KeyPrefix),
		zap.String("key", m.Key))
	return nil
}

// Cleanup closes the Redis client when the module is unloaded.
func (m *RedisRouter) Cleanup() error {
	if m.client != nil {
		return m.client.Close()
	}
	return nil
}

// ServeHTTP looks up the route in Redis, stores the upstream in the configured
// variable, copies any route headers onto the request, then calls next.
func (m *RedisRouter) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	repl := r.Context().Value(caddy.ReplacerCtxKey).(*caddy.Replacer)
	key := m.KeyPrefix + repl.ReplaceAll(m.Key, "")

	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(m.Timeout))
	defer cancel()

	raw, err := m.client.Get(ctx, key).Result()
	switch {
	case err == redis.Nil:
		if m.OnMiss < 0 {
			return next.ServeHTTP(w, r)
		}
		return caddyhttp.Error(m.OnMiss, fmt.Errorf("redis_router: no route for %q", key))
	case err != nil:
		return caddyhttp.Error(http.StatusBadGateway,
			fmt.Errorf("redis_router: lookup failed for %q: %w", key, err))
	}

	var rt route
	if jsonErr := json.Unmarshal([]byte(raw), &rt); jsonErr != nil {
		// Tolerate a bare "host:port" string value (no headers).
		rt = route{Upstream: raw}
	}
	if rt.Upstream == "" {
		return caddyhttp.Error(http.StatusBadGateway,
			fmt.Errorf("redis_router: empty upstream for %q", key))
	}

	caddyhttp.SetVar(r.Context(), m.Var, rt.Upstream)
	for k, v := range rt.Headers {
		r.Header.Set(k, v)
	}
	return next.ServeHTTP(w, r)
}

// UnmarshalCaddyfile parses the redis_router directive:
//
//	redis_router [<address>] {
//	    address     127.0.0.1:6379
//	    password    {env.REDIS_PASSWORD}
//	    db          0
//	    key_prefix  route:
//	    key         {http.request.host}
//	    var         upstream
//	    on_miss     503            # or "passthrough"
//	    timeout     2s
//	}
func (m *RedisRouter) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		if d.NextArg() {
			m.Address = d.Val()
		}
		for d.NextBlock(0) {
			switch d.Val() {
			case "address":
				if !d.NextArg() {
					return d.ArgErr()
				}
				m.Address = d.Val()
			case "password":
				if !d.NextArg() {
					return d.ArgErr()
				}
				m.Password = d.Val()
			case "db":
				if !d.NextArg() {
					return d.ArgErr()
				}
				n, err := strconv.Atoi(d.Val())
				if err != nil {
					return d.Errf("invalid db %q: %v", d.Val(), err)
				}
				m.DB = n
			case "key_prefix":
				if !d.NextArg() {
					return d.ArgErr()
				}
				m.KeyPrefix = d.Val()
			case "key":
				if !d.NextArg() {
					return d.ArgErr()
				}
				m.Key = d.Val()
			case "var":
				if !d.NextArg() {
					return d.ArgErr()
				}
				m.Var = d.Val()
			case "on_miss":
				if !d.NextArg() {
					return d.ArgErr()
				}
				if d.Val() == "passthrough" {
					m.OnMiss = -1
				} else {
					n, err := strconv.Atoi(d.Val())
					if err != nil {
						return d.Errf("invalid on_miss %q: %v", d.Val(), err)
					}
					m.OnMiss = n
				}
			case "timeout":
				if !d.NextArg() {
					return d.ArgErr()
				}
				dur, err := caddy.ParseDuration(d.Val())
				if err != nil {
					return d.Errf("invalid timeout %q: %v", d.Val(), err)
				}
				m.Timeout = caddy.Duration(dur)
			default:
				return d.Errf("unknown redis_router option %q", d.Val())
			}
		}
	}
	return nil
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m RedisRouter
	err := m.UnmarshalCaddyfile(h.Dispenser)
	return &m, err
}

// Interface guards.
var (
	_ caddy.Provisioner           = (*RedisRouter)(nil)
	_ caddy.CleanerUpper          = (*RedisRouter)(nil)
	_ caddyhttp.MiddlewareHandler = (*RedisRouter)(nil)
	_ caddyfile.Unmarshaler       = (*RedisRouter)(nil)
)
