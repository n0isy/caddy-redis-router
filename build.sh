#!/usr/bin/env bash
# Build & validate caddy-redis-router locally — no native Go toolchain required,
# everything runs inside the `caddy:2-builder` Docker image.
#
# It (1) tidies the module's go.mod/go.sum, then (2) xcaddy-builds a Caddy binary
# from the LOCAL source (via a replace directive) together with the same plugin
# set the ctotwin.com host runs, pinned to the same Caddy core version. Finally it
# lists the modules so you can eyeball that redis_router linked in.
#
# Usage:  ./build.sh            # tidy + build ./caddy-test + list modules
set -euo pipefail

CADDY_VERSION="${CADDY_VERSION:-v2.11.4}"   # match the production host
BUILDER_IMAGE="caddy:2-builder"
SRC="$(cd "$(dirname "$0")" && pwd)"

echo "==> go mod tidy (inside $BUILDER_IMAGE)"
docker run --rm -v "$SRC":/src -w /src --entrypoint sh "$BUILDER_IMAGE" -c '
  set -e
  go mod tidy
'

echo "==> xcaddy build caddy $CADDY_VERSION + redis_router (local) + digitalocean + jwt"
docker run --rm -v "$SRC":/src -w /src --entrypoint sh "$BUILDER_IMAGE" -c "
  set -e
  xcaddy build $CADDY_VERSION \
    --with github.com/n0isy/caddy-redis-router=/src \
    --with github.com/caddy-dns/digitalocean \
    --with github.com/ggicci/caddy-jwt \
    --output /src/caddy-test
"

echo "==> modules present in the built binary:"
docker run --rm -v "$SRC":/src -w /src --entrypoint sh "$BUILDER_IMAGE" -c '
  /src/caddy-test list-modules 2>/dev/null | grep -iE "redis_router|digitalocean|authentication.providers.jwt" || {
    echo "!! expected modules missing"; exit 1; }
'
echo "==> OK — caddy-test built at $SRC/caddy-test"
