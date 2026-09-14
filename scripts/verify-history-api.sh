#!/bin/sh
# Read-only checks against the mock Compose stack; do not print history bodies.
set -eu
. ./scripts/admin-session.sh
auth_init 'http://127.0.0.1:8081/admin/v1'
admin_url=http://127.0.0.1:8081/admin/v1

get() {
  auth_curl --fail --silent --show-error --max-time 5 "$@"
}

ready=false
for attempt in $(seq 1 60); do
  if get "$admin_url/health" 2>/dev/null | jq -e '.status == "ok"' >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
test "$ready" = true

first="$(get "$admin_url/requests?limit=2")"
printf '%s' "$first" | jq -e '
  (.requests | length) == 2 and (.next_cursor | type) == "string" and
  all(.requests[]; has("attempts") | not)
' >/dev/null
cursor="$(printf '%s' "$first" | jq -r '.next_cursor')"
second="$(get --get --data-urlencode 'limit=2' --data-urlencode "cursor=$cursor" "$admin_url/requests")"
printf '%s' "$second" | jq -e --argjson first "$first" '
  (.requests | length) == 2 and
  all(.requests[]; .request_id as $id | all($first.requests[]; .request_id != $id))
' >/dev/null

# Parameter quoting is performed by psql, not shell-built SQL literals.
for id in $(printf '%s' "$first" | jq -r '.requests[].request_id'); do
  test "$(docker compose exec -T postgres psql -v ON_ERROR_STOP=1 \
    -v id="$id" -U routeforge_local -d routeforge -At <<'SQL'
SELECT count(*) FROM routeforge_requests WHERE request_id = :'id';
SQL
  )" -eq 1
done

# Choose a provider-served record to exercise nullable attempt decoding too.
served="$(get "$admin_url/requests?cache_hit=false&limit=1")"
id="$(printf '%s' "$served" | jq -er '.requests[0].request_id')"
detail="$(get "$admin_url/requests/$id")"
database_attempts="$(docker compose exec -T postgres psql -v ON_ERROR_STOP=1 \
  -v id="$id" -U routeforge_local -d routeforge -At <<'SQL'
SELECT coalesce(json_agg(json_build_object(
  'attempt_number', attempt_number, 'provider', provider,
  'resolved_provider_model', resolved_provider_model, 'fallback', fallback,
  'duration_us', duration_us, 'ttfc_us', ttfc_us, 'outcome', outcome,
  'input_tokens', input_tokens, 'output_tokens', output_tokens,
  'total_tokens', total_tokens, 'estimated_cost_micro_usd', estimated_cost_micro_usd
) ORDER BY attempt_number), '[]')
FROM routeforge_provider_attempts WHERE request_id = :'id';
SQL
)"
printf '%s' "$detail" | jq -e --arg id "$id" --argjson attempts "$database_attempts" '
  .request_id == $id and .attempt_count == (.attempts | length) and
  [.attempts[] | del(.started_at, .completed_at)] == $attempts
' >/dev/null

streamed="$(get "$admin_url/requests?streaming=true&limit=1")"
id="$(printf '%s' "$streamed" | jq -er '.requests[0].request_id')"
streamed_detail="$(get "$admin_url/requests/$id")"
printf '%s' "$streamed_detail" | jq -e '
  .streaming and (.attempts | length) > 0 and
  all(.attempts[]; .ttfc_us != null and .ttfc_us <= .duration_us)
' >/dev/null

cached="$(get "$admin_url/requests?cache_hit=true&limit=1")"
id="$(printf '%s' "$cached" | jq -er '.requests[0].request_id')"
cached_detail="$(get "$admin_url/requests/$id")"
printf '%s' "$cached_detail" | jq -e '
  .cache_hit and .outcome == "success" and .attempt_count == 0 and .attempts == []
' >/dev/null

test "$(auth_curl --silent --show-error --max-time 5 -o /dev/null -w '%{http_code}' \
  "$admin_url/requests/invalid")" = 400
test "$(auth_curl --silent --show-error --max-time 5 -o /dev/null -w '%{http_code}' \
  "$admin_url/requests/rfreq_AAAAAAAAAAAAAAAAAAAAAA")" = 404

# Allowlist the complete response contract rather than searching only for a
# handful of forbidden content names.
for document in "$first" "$second" "$detail" "$cached_detail" "$streamed_detail"; do
  printf '%s' "$document" | jq -e '
    ["requests", "next_cursor", "request_id", "started_at", "completed_at",
     "routing_policy", "streaming", "logical_model", "initial_provider",
     "final_provider", "outcome", "attempt_count", "fallback_count",
     "request_duration_us", "cache_hit", "attempts", "attempt_number",
     "provider", "resolved_provider_model", "fallback", "duration_us",
     "ttfc_us", "input_tokens", "output_tokens", "total_tokens",
     "estimated_cost_micro_usd"] as $allowed |
    all(.. | objects | keys[]; . as $key | $allowed | index($key) != null)
  ' >/dev/null
done
echo "Admin history list, pagination, detail, cache hits, errors, and privacy contract verified"
