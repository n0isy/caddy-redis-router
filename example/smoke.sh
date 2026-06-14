#!/usr/bin/env bash
# End-to-end smoke test: prove the locally-built ../caddy-test resolves an
# upstream from Redis by Host, injects the route's header, strips a spoofed
# inbound one, and 503s on a missing route. Requires ../caddy-test (run ../build.sh).
set -euo pipefail
cd "$(dirname "$0")"
B=127.0.0.1:8099
C="docker compose -f compose.smoke.yaml"

[ -f ../caddy-test ] || { echo "missing ../caddy-test — run ../build.sh first"; exit 1; }

cleanup() { $C down -v >/dev/null 2>&1 || true; }
trap cleanup EXIT
$C up -d

echo "==> waiting for caddy..."
for i in $(seq 1 30); do
  curl -fsS -o /dev/null "http://$B/" -H "Host: warmup" 2>/dev/null && break || sleep 1
done

echo "==> seed route table in redis"
$C exec -T redis redis-cli SET route:app.test \
  '{"upstream":"backend:80","headers":{"X-Internal-Auth":"s3cr3t-smoke"}}' >/dev/null

fail=0

echo "==> 1) known host resolves to backend + header injected"
out=$(curl -fsS "http://$B/" -H "Host: app.test")
echo "$out" | grep -q "X-Internal-Auth: s3cr3t-smoke" \
  && echo "   PASS: header injected" || { echo "   FAIL: header missing"; fail=1; }

echo "==> 2) spoofed inbound X-Internal-Auth is overwritten by the router"
out=$(curl -fsS "http://$B/" -H "Host: app.test" -H "X-Internal-Auth: HACKER")
echo "$out" | grep -q "X-Internal-Auth: HACKER" \
  && { echo "   FAIL: spoof leaked through"; fail=1; } \
  || echo "   PASS: spoof stripped (got router value)"

echo "==> 3) unknown host -> 503"
code=$(curl -s -o /dev/null -w '%{http_code}' "http://$B/" -H "Host: nope.test")
[ "$code" = "503" ] && echo "   PASS: 503 on miss" || { echo "   FAIL: got $code"; fail=1; }

echo
[ "$fail" = 0 ] && echo "ALL SMOKE TESTS PASSED" || { echo "SMOKE TESTS FAILED"; exit 1; }
