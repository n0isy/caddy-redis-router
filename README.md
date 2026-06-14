# caddy-redis-router

A [Caddy](https://caddyserver.com) HTTP handler that resolves a request's
**upstream address** (and optional **per-route headers**) from **Redis**, at
request time. It turns a static `Caddyfile` into a data-driven router: the route
table lives in Redis and changes at runtime with **zero Caddy reloads**.

For each request the handler:

1. builds a key — `<key_prefix>` + an expanded placeholder (default the request
   `Host`),
2. `GET`s that key from Redis,
3. stores the upstream in a Caddy variable (`{http.vars.upstream}` by default)
   for `reverse_proxy` to dial,
4. copies any route headers onto the request (e.g. an internal auth token),
5. calls the next handler.

## Redis value format

JSON:

```json
{ "upstream": "10.1.2.3:8080", "headers": { "X-Internal-Auth": "s3cr3t" } }
```

A bare `host:port` string (no headers) is also accepted.

```
> SET route:acme-web.example.com '{"upstream":"10.1.2.3:8080","headers":{"X-Internal-Auth":"s3cr3t"}}'
```

## Caddyfile

```caddyfile
{
    order redis_router before reverse_proxy
}

*.example.com {
    redis_router {
        address    127.0.0.1:6379
        password   {env.REDIS_PASSWORD}
        db         0
        key_prefix "route:"
        key        {http.request.host}
        var        upstream
        on_miss    503          # HTTP status on a key miss; or "passthrough"
        timeout    2s
    }
    reverse_proxy {http.vars.upstream}
}
```

All options are optional; the defaults above are applied. The shorthand
`redis_router 127.0.0.1:6379` sets just the address.

| Option       | Default               | Meaning                                                        |
|--------------|-----------------------|----------------------------------------------------------------|
| `address`    | `127.0.0.1:6379`      | Redis server `host:port`.                                      |
| `password`   | _(none)_              | Redis AUTH; placeholders OK (`{env.REDIS_PASSWORD}`).         |
| `db`         | `0`                   | Redis database index.                                          |
| `key_prefix` | `route:`              | Prepended to the looked-up key.                               |
| `key`        | `{http.request.host}` | Placeholder-expanded lookup key.                              |
| `var`        | `upstream`            | Caddy var the upstream is stored in → `{http.vars.<var>}`.    |
| `on_miss`    | `503`                 | HTTP status when the key is absent, or `passthrough`.        |
| `timeout`    | `2s`                  | Per-lookup Redis timeout.                                      |

On a key **miss** the handler returns `on_miss` (default `503`) so a
`handle_errors` block can present a friendly page; set `on_miss passthrough` to
fall through to the next handler instead. A Redis **error** returns `502`.

## Security note

The handler **sets** the configured route headers on the request before
proxying. If a header carries trust (an internal auth token), make sure the
surrounding site block **strips the inbound header** first, so a client cannot
spoof it:

```caddyfile
request_header -X-Internal-Auth
redis_router { ... }
reverse_proxy {http.vars.upstream}
```

## Build

Built into Caddy with [xcaddy](https://github.com/caddyserver/xcaddy):

```sh
xcaddy build --with github.com/n0isy/caddy-redis-router
```

`build.sh` builds and smoke-tests a binary locally using the `caddy:2-builder`
Docker image (no local Go toolchain required).

## License

MIT
