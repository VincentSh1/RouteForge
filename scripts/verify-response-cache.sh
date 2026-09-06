#!/bin/sh
# Runs against the clean mock-only Compose smoke stack. Never prints responses,
# Redis contents, keys, connection URLs, or operational record identifiers.
set -eu

sql() {
  docker compose exec -T postgres psql -v ON_ERROR_STOP=1 -U routeforge_local -d routeforge -Atqc "$1"
}

wait_count() {
  query="$1"
  expected="$2"
  for attempt in $(seq 1 60); do
    if [ "$(sql "$query")" -eq "$expected" ]; then return 0; fi
    sleep 1
  done
  echo "Cache history did not reach the expected count" >&2
  return 1
}

redis_ready() {
  for attempt in $(seq 1 60); do
    if docker compose exec -T redis redis-cli ping 2>/dev/null | grep -qx PONG; then return 0; fi
    sleep 1
  done
  echo "Redis did not become healthy" >&2
  return 1
}

request() {
  response="$(curl --fail --silent --show-error --max-time 10 \
    -H 'Content-Type: application/json' \
    --data '{"model":"routeforge/general","messages":[{"role":"user","content":"RouteForge cache smoke scenario"}]}' \
    http://127.0.0.1:8080/v1/chat/completions)"
  printf '%s' "$response" |
    jq -e '.object == "chat.completion" and .choices[0].finish_reason == "stop"' >/dev/null
}

metric() {
  docker compose exec -T routeforge wget -q -O - http://127.0.0.1:9090/metrics |
    awk -v family="$1" -v result="$2" '
      index($1, family "{") == 1 && index($1, "result=\"" result "\"") {sum += $2}
      END {printf "%.0f\n", sum}'
}

redis_ready
requests_before="$(sql 'SELECT count(*) FROM routeforge_requests;')"
attempts_before="$(sql 'SELECT count(*) FROM routeforge_provider_attempts;')"
hits_before="$(sql 'SELECT count(*) FROM routeforge_requests WHERE cache_hit;')"
miss_metric="$(metric routeforge_cache_lookups_total miss)"
write_metric="$(metric routeforge_cache_writes_total success)"
request
wait_count 'SELECT count(*) FROM routeforge_requests;' "$((requests_before + 1))"
test "$(sql 'SELECT count(*) FROM routeforge_provider_attempts;')" -eq "$((attempts_before + 1))"
test "$(metric routeforge_cache_lookups_total miss)" -eq "$((miss_metric + 1))"
test "$(metric routeforge_cache_writes_total success)" -eq "$((write_metric + 1))"

hit_metric="$(metric routeforge_cache_lookups_total hit)"
request
wait_count 'SELECT count(*) FROM routeforge_requests WHERE cache_hit;' "$((hits_before + 1))"
test "$(sql 'SELECT count(*) FROM routeforge_provider_attempts;')" -eq "$((attempts_before + 1))"
test "$(metric routeforge_cache_lookups_total hit)" -eq "$((hit_metric + 1))"
test "$(sql 'SELECT count(*) FROM routeforge_requests WHERE cache_hit AND (attempt_count <> 0 OR final_provider <> '\''mock'\'' OR outcome <> '\''success'\'');')" -eq 0
test "$(sql 'SELECT count(*) FROM routeforge_provider_attempts a JOIN routeforge_requests r USING (request_id) WHERE r.cache_hit;')" -eq 0
echo "Redis miss/write/hit and PostgreSQL cache history verified"

# Restart only RouteForge; both PostgreSQL rows and Redis entries should remain.
rows_before_restart="$(sql 'SELECT count(*) FROM routeforge_requests;')"
docker compose restart routeforge >/dev/null
ready=false
for attempt in $(seq 1 60); do
  if curl --fail --silent --max-time 2 http://127.0.0.1:8080/health >/dev/null; then ready=true; break; fi
  sleep 1
done
test "$ready" = true
test "$(sql 'SELECT count(*) FROM routeforge_requests;')" -eq "$rows_before_restart"
request
wait_count 'SELECT count(*) FROM routeforge_requests WHERE cache_hit;' "$((hits_before + 2))"
test "$(sql 'SELECT count(*) FROM routeforge_provider_attempts;')" -eq "$((attempts_before + 1))"

# Always restore Redis if the fail-open check fails; workflow cleanup also runs.
trap 'docker compose start redis >/dev/null 2>&1 || true' EXIT
docker compose stop redis >/dev/null
error_metric="$(metric routeforge_cache_lookups_total error)"
request
wait_count 'SELECT count(*) FROM routeforge_provider_attempts;' "$((attempts_before + 2))"
test "$(metric routeforge_cache_lookups_total error)" -eq "$((error_metric + 1))"
docker compose start redis >/dev/null
redis_ready
trap - EXIT

# Ephemeral Redis lost its data. Wait for a successful write as pooled sockets
# recover, then prove the next identical request avoids another invocation.
write_metric="$(metric routeforge_cache_writes_total success)"
recovered=false
for attempt in $(seq 1 20); do
  request
  if [ "$(metric routeforge_cache_writes_total success)" -gt "$write_metric" ]; then recovered=true; break; fi
  sleep 1
done
test "$recovered" = true
hits_before_recovery="$(metric routeforge_cache_lookups_total hit)"
request
test "$(metric routeforge_cache_lookups_total hit)" -eq "$((hits_before_recovery + 1))"
echo "Redis fail-open, recovery, and RouteForge restart reuse verified"
