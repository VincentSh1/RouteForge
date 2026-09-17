#!/bin/sh
# Exercise the built console and same-origin proxy, never print record bodies.
set -eu
. ./scripts/admin-session.sh
console_url=http://127.0.0.1:3001
ready=false
for attempt in $(seq 1 60); do
  if command curl --fail --silent --max-time 3 "$console_url/health" >/dev/null; then ready=true; break; fi
  sleep 1
done
test "$ready" = true

for path in requests overview benchmarks routing/config; do
  test "$(command curl --silent --max-time 5 -o /dev/null -w '%{http_code}' "$console_url/api/$path")" = 401
  test "$(command curl --silent --max-time 5 -o /dev/null -w '%{http_code}' "http://127.0.0.1:8081/admin/v1/$path")" = 401
done
test "$(command curl --silent --max-time 5 -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' --data '{}' "$console_url/api/auth/login")" = 403
test "$(command curl --silent --max-time 5 -o /dev/null -w '%{http_code}' -H 'Origin: http://127.0.0.1:3001' -H 'Content-Type: application/json' --data '{"secret":"invalid"}' "$console_url/api/auth/login")" = 401
auth_init 'http://127.0.0.1:3001/api'
grep -q '^#HttpOnly_' "$auth_cookie_jar"
auth_curl --fail --silent --max-time 5 "$console_url/api/auth/session" | jq -e '.enabled and .authenticated' >/dev/null
html="$(auth_curl --fail --silent --show-error --max-time 5 "$console_url/")"
printf '%s' "$html" | grep -q 'RouteForge Console'
asset="$(printf '%s' "$html" | sed -n 's/.*src="\(\/assets\/[^"]*\.js\)".*/\1/p')"
test -n "$asset"
auth_curl --fail --silent --show-error --max-time 5 "$console_url$asset" >/dev/null
page="$(auth_curl --fail --silent --show-error --max-time 5 "$console_url/api/requests?limit=2&cache_hit=true")"
printf '%s' "$page" | jq -e '(.requests | length) > 0 and all(.requests[]; .cache_hit)' >/dev/null
id="$(printf '%s' "$page" | jq -er '.requests[0].request_id')"
detail="$(auth_curl --fail --silent --show-error --max-time 5 "$console_url/api/requests/$id")"
direct="$(auth_curl --fail --silent --show-error --max-time 5 "http://127.0.0.1:8081/admin/v1/requests/$id")"
printf '%s' "$detail" | jq -e --argjson direct "$direct" '. == $direct and .cache_hit and .attempt_count == 0 and .attempts == []' >/dev/null
auth_curl --fail --silent --show-error --max-time 5 "$console_url/requests/$id" | grep -q 'RouteForge Console'
test "$(auth_curl --silent --max-time 5 -X POST -o /dev/null -w '%{http_code}' "$console_url/api/requests")" = 405
test "$(auth_curl --silent --max-time 5 -o /dev/null -w '%{http_code}' "$console_url/api/unsupported")" = 404
echo "Console assets, detail deep-link, read-only proxy, and real cache-hit history verified"

state="$(auth_curl --fail --silent --show-error --max-time 5 "$console_url/api/overview")"
printf '%s' "$state" | jq -e '
  .routing.policy == "deterministic" and .routing.provider_order == ["mock"] and
  .features.cache and .features.persistence and .features.metrics and (.features.tracing | not) and
  (.providers | length) == 1 and .providers[0].provider == "mock" and
  .providers[0].circuit_state == "closed" and .providers[0].eligible and
  .providers[0].complete_price_models == 1
' >/dev/null
auth_curl --fail --silent --show-error --max-time 5 http://127.0.0.1:8081/admin/v1/overview |
  jq -e '.providers[0].provider == "mock"' >/dev/null
for page in overview providers; do
  auth_curl --fail --silent --show-error --max-time 5 "$console_url/$page" | grep -q 'RouteForge Console'
done
test "$(auth_curl --silent --max-time 5 -X POST -o /dev/null -w '%{http_code}' "$console_url/api/overview")" = 405
echo "Current provider/routing state and console operations routes verified"

catalog="$(auth_curl --fail --silent --show-error --max-time 5 "$console_url/api/benchmarks")"
printf '%s' "$catalog" | jq -e '[.scenarios[].id] == ["stable", "degradation", "rate_limit", "streaming", "cold_start"]' >/dev/null
before="$(auth_curl --fail --silent --show-error --max-time 5 "$console_url/api/overview" | jq -c 'del(.observed_at)')"
for scenario in stable degradation rate_limit streaming cold_start; do
  for state in warm cold; do
    report="$(auth_curl --fail --silent --show-error --max-time 5 "$console_url/api/benchmarks/$scenario?state=$state")"
    printf '%s' "$report" | jq -e --arg scenario "$scenario" --arg state "$state" '
      .scenario == $scenario and .state == $state and .scenario_version == 1 and
      [.results[].policy] == ["deterministic", "latency", "cost", "cost_latency"] and
      all(.results[]; .requests > 0 and .estimated_cost_micro_usd > 0) and
      (if $scenario == "streaming" then all(.results[]; has("p50_ttfc_ms") and (has("p50_latency_ms") | not))
       else all(.results[]; has("p50_latency_ms") and (has("p50_ttfc_ms") | not)) end)
    ' >/dev/null
    repeat="$(auth_curl --fail --silent --show-error --max-time 5 "$console_url/api/benchmarks/$scenario?state=$state")"
    test "$report" = "$repeat"
  done
done
after="$(auth_curl --fail --silent --show-error --max-time 5 "$console_url/api/overview" | jq -c 'del(.observed_at)')"
test "$before" = "$after"
auth_curl --fail --silent --show-error --max-time 5 "$console_url/benchmarks" | grep -q 'RouteForge Console'
test "$(auth_curl --silent --max-time 5 -o /dev/null -w '%{http_code}' "$console_url/api/benchmarks/unknown")" = 400
test "$(auth_curl --silent --max-time 5 -X POST -o /dev/null -w '%{http_code}' "$console_url/api/benchmarks/stable")" = 405
echo "Built-in benchmark comparisons, reproducibility, isolation, and console proxy verified"

original="$(auth_curl --fail --silent --max-time 5 "$console_url/api/routing/config")"
updated="$(printf '%s' "$original" | jq -c '.policy = "cost"')"
test "$(auth_curl --silent --max-time 5 -X PUT -H 'Content-Type: application/json' --data "$updated" -o /dev/null -w '%{http_code}' "$console_url/api/routing/config")" = 403
printf '%s' "$updated" | auth_curl --fail --silent --max-time 5 -X PUT -H 'Origin: http://127.0.0.1:3001' -H 'Content-Type: application/json' --data-binary @- "$console_url/api/routing/config" | jq -e '.policy == "cost"' >/dev/null
auth_curl --fail --silent --max-time 5 "$console_url/api/routing/config" | jq -e '.policy == "cost"' >/dev/null
auth_curl --fail --silent --max-time 5 "$console_url/api/overview" | jq -e '.routing.policy == "cost"' >/dev/null
# Streaming bypasses Redis, proving a real mock attempt uses the new policy.
command curl --fail --silent --max-time 10 -H 'Content-Type: application/json' --data '{"model":"routeforge/general","stream":true,"messages":[{"role":"user","content":"RouteForge routing smoke"}]}' http://127.0.0.1:8080/v1/chat/completions >/dev/null
recorded=false
for attempt in $(seq 1 30); do
  if auth_curl --fail --silent --max-time 5 "$console_url/api/requests?routing_policy=cost&streaming=true&limit=1" | jq -e '.requests | any(.routing_policy == "cost" and .attempt_count == 1)' >/dev/null; then recorded=true; break; fi
  sleep 1
done
# Restore before assertions so normal smoke failures do not leave changed settings.
printf '%s' "$original" | auth_curl --fail --silent --max-time 5 -X PUT -H 'Origin: http://127.0.0.1:3001' -H 'Content-Type: application/json' --data-binary @- "$console_url/api/routing/config" >/dev/null
test "$recorded" = true
restored="$(auth_curl --fail --silent --max-time 5 "$console_url/api/routing/config" | jq -S -c .)"
test "$restored" = "$(printf '%s' "$original" | jq -S -c .)"
auth_curl --fail --silent --max-time 5 "$console_url/routing" | grep -q 'RouteForge Console'
echo "Authenticated routing update, Origin rejection, real mock policy history, and restoration verified"

auth_curl --fail --silent --max-time 5 -H 'Origin: http://127.0.0.1:3001' -H 'Content-Type: application/json' \
  --data '{}' "$console_url/api/auth/logout" | jq -e '.authenticated == false' >/dev/null
test "$(auth_curl --silent --max-time 5 -o /dev/null -w '%{http_code}' "$console_url/api/overview")" = 401
auth_login
auth_curl --fail --silent --max-time 5 "$console_url/api/overview" >/dev/null
echo "Admin authentication, Origin protection, session cookie, logout, and re-login verified"
