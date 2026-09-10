#!/bin/sh
# Exercise the built console and same-origin proxy, never print record bodies.
set -eu
console_url=http://127.0.0.1:3001
ready=false
for attempt in $(seq 1 60); do
  if curl --fail --silent --max-time 3 "$console_url/health" >/dev/null; then ready=true; break; fi
  sleep 1
done
test "$ready" = true
html="$(curl --fail --silent --show-error --max-time 5 "$console_url/")"
printf '%s' "$html" | grep -q 'RouteForge Console'
asset="$(printf '%s' "$html" | sed -n 's/.*src="\(\/assets\/[^"]*\.js\)".*/\1/p')"
test -n "$asset"
curl --fail --silent --show-error --max-time 5 "$console_url$asset" >/dev/null
page="$(curl --fail --silent --show-error --max-time 5 "$console_url/api/requests?limit=2&cache_hit=true")"
printf '%s' "$page" | jq -e '(.requests | length) > 0 and all(.requests[]; .cache_hit)' >/dev/null
id="$(printf '%s' "$page" | jq -er '.requests[0].request_id')"
detail="$(curl --fail --silent --show-error --max-time 5 "$console_url/api/requests/$id")"
direct="$(curl --fail --silent --show-error --max-time 5 "http://127.0.0.1:8081/admin/v1/requests/$id")"
printf '%s' "$detail" | jq -e --argjson direct "$direct" '. == $direct and .cache_hit and .attempt_count == 0 and .attempts == []' >/dev/null
curl --fail --silent --show-error --max-time 5 "$console_url/requests/$id" | grep -q 'RouteForge Console'
test "$(curl --silent --max-time 5 -X POST -o /dev/null -w '%{http_code}' "$console_url/api/requests")" = 405
test "$(curl --silent --max-time 5 -o /dev/null -w '%{http_code}' "$console_url/api/unsupported")" = 404
echo "Console assets, detail deep-link, read-only proxy, and real cache-hit history verified"
