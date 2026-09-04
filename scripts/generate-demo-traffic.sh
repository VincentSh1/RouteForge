#!/bin/sh

set -eu

request_count="${1:-40}"
case "$request_count" in
  ''|*[!0-9]*)
    echo "usage: $0 [request-count from 1 to 200]" >&2
    exit 2
    ;;
esac
if [ "$request_count" -lt 1 ] || [ "$request_count" -gt 200 ]; then
  echo "request count must be between 1 and 200" >&2
  exit 2
fi
if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
  echo "Docker with Compose is required to generate local demo traffic" >&2
  exit 1
fi

base_url="http://127.0.0.1:8080"
if ! docker compose exec -T routeforge \
  wget -q -O /dev/null -T 5 "$base_url/health"; then
  echo "RouteForge is unavailable at $base_url" >&2
  exit 1
fi

request_number=1
while [ "$request_number" -le "$request_count" ]; do
  if [ $((request_number % 2)) -eq 0 ]; then
    payload='{"model":"routeforge/general","messages":[{"role":"user","content":"RouteForge streaming demo request"}],"stream":true}'
  else
    payload='{"model":"routeforge/general","messages":[{"role":"user","content":"RouteForge demo request"}]}'
  fi

  if ! docker compose exec -T routeforge \
    wget -q -O /dev/null -T 10 \
    --header='Content-Type: application/json' \
    --post-data="$payload" \
    "$base_url/v1/chat/completions"; then
    echo "demo request $request_number failed" >&2
    exit 1
  fi

  request_number=$((request_number + 1))
done

echo "Generated $request_count local mock requests (synchronous and streaming)."
