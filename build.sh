#!/usr/bin/env bash
# Build & validate caddy-redis-router locally — no native Go toolchain required,
# everything runs inside a versioned Caddy builder Docker image.
#
# It xcaddy-builds a Caddy binary without modifying go.mod/go.sum in this checkout,
# from the LOCAL source (via a replace directive) together with the same plugin
# set the desk.cloud host runs, pinned to the same Caddy core version. Finally it
# checks each required module.
#
# Usage:  ./build.sh            # build ./caddy-test + check required modules
set -euo pipefail

CADDY_VERSION="${CADDY_VERSION:-v2.11.4}"   # match the production host
BUILDER_IMAGE="${BUILDER_IMAGE:-caddy:2.11.4-builder}"
DNS_VERSION="${DNS_VERSION:-04bde2867106}"
JWT_VERSION="${JWT_VERSION:-v1.2.0}"
SRC="$(cd "$(dirname "$0")" && pwd)"

echo "==> xcaddy build caddy $CADDY_VERSION + redis_router (local) + digitalocean + jwt"
docker run --rm -v "$SRC":/src:ro -v "$SRC":/out -w /tmp --entrypoint sh "$BUILDER_IMAGE" -c "
  set -e
  xcaddy build $CADDY_VERSION \
    --with github.com/n0isy/caddy-redis-router=/src \
    --with github.com/caddy-dns/digitalocean@${DNS_VERSION} \
    --with github.com/ggicci/caddy-jwt@${JWT_VERSION} \
    --output /out/caddy-test
"

echo "==> modules present in the built binary:"
docker run --rm -v "$SRC":/src -w /src --entrypoint sh "$BUILDER_IMAGE" -c '
  set -e
  modules="$(/src/caddy-test list-modules 2>/dev/null)"
  for module in dns.providers.digitalocean http.authentication.providers.jwt http.handlers.redis_router; do
    printf "%s\n" "$modules" | grep -qx "$module" || {
      echo "!! required module missing: $module" >&2; exit 1;
    }
    echo "   ok: $module"
  done
'
echo "==> OK — caddy-test built at $SRC/caddy-test"
