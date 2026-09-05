#!/bin/sh

set -eu

routeforge_url="http://127.0.0.1:8080"
prometheus_url="http://127.0.0.1:9091"
grafana_url="http://127.0.0.1:3000"
poll_attempts=60
retry_seconds=2

for command_name in curl jq docker; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "$command_name is required to verify the observability stack" >&2
    exit 1
  fi
done
if ! docker compose version >/dev/null 2>&1; then
  echo "Docker Compose is required to verify the observability stack" >&2
  exit 1
fi

wait_for_url() {
  service_name="$1"
  url="$2"
  attempt=1
  while [ "$attempt" -le "$poll_attempts" ]; do
    if curl --fail --silent --show-error --max-time 5 "$url" >/dev/null 2>&1; then
      echo "$service_name is ready"
      return 0
    fi
    sleep "$retry_seconds"
    attempt=$((attempt + 1))
  done
  echo "$service_name did not become ready within $((poll_attempts * retry_seconds)) seconds" >&2
  return 1
}

wait_for_routeforge_target() {
  attempt=1
  while [ "$attempt" -le "$poll_attempts" ]; do
    if curl --fail --silent --show-error --max-time 5 \
      "$prometheus_url/api/v1/targets" 2>/dev/null |
      jq -e '
        .status == "success" and
        any(
          .data.activeTargets[];
          .labels.job == "routeforge" and
          .health == "up" and
          (.scrapeUrl | contains("routeforge:9090"))
        )
      ' >/dev/null 2>&1; then
      echo "Prometheus reports the RouteForge target UP"
      return 0
    fi
    sleep "$retry_seconds"
    attempt=$((attempt + 1))
  done
  echo "Prometheus did not report the RouteForge target UP" >&2
  return 1
}

wait_for_positive_metric() {
  metric_name="$1"
  attempt=1
  while [ "$attempt" -le "$poll_attempts" ]; do
    if curl --fail --silent --show-error --max-time 5 --get \
      --data-urlencode "query=sum($metric_name)" \
      "$prometheus_url/api/v1/query" 2>/dev/null |
      jq -e '
        .status == "success" and
        (.data.result | length) > 0 and
        (.data.result[0].value[1] | tonumber) > 0
      ' >/dev/null 2>&1; then
      echo "$metric_name contains data"
      return 0
    fi
    sleep "$retry_seconds"
    attempt=$((attempt + 1))
  done
  echo "$metric_name did not contain a positive value" >&2
  return 1
}

wait_for_grafana_dashboard() {
  attempt=1
  while [ "$attempt" -le "$poll_attempts" ]; do
    if curl --fail --silent --show-error --max-time 5 \
      "$grafana_url/api/dashboards/uid/routeforge-overview" 2>/dev/null |
      jq -e '
        .dashboard.uid == "routeforge-overview" and
        .dashboard.title == "RouteForge Overview"
      ' >/dev/null 2>&1; then
      echo "Grafana provisioned the RouteForge Overview dashboard"
      return 0
    fi
    sleep "$retry_seconds"
    attempt=$((attempt + 1))
  done
  echo "Grafana did not provision the RouteForge Overview dashboard" >&2
  return 1
}

postgres_value() {
  docker compose exec -T postgres \
    psql -v ON_ERROR_STOP=1 -U routeforge_local -d routeforge -Atqc "$1"
}

wait_for_postgres_count() {
  table_name="$1"
  minimum="$2"
  attempt=1
  while [ "$attempt" -le "$poll_attempts" ]; do
    count="$(postgres_value "SELECT count(*) FROM $table_name;")"
    if [ "$count" -ge "$minimum" ]; then
      echo "$table_name contains $count rows"
      return 0
    fi
    sleep "$retry_seconds"
    attempt=$((attempt + 1))
  done
  echo "$table_name did not reach $minimum rows" >&2
  return 1
}

wait_for_url "RouteForge" "$routeforge_url/health"
wait_for_url "Prometheus" "$prometheus_url/-/ready"
wait_for_url "Grafana" "$grafana_url/api/health"
wait_for_routeforge_target

requests_before_traffic="$(postgres_value 'SELECT count(*) FROM routeforge_requests;')"
attempts_before_traffic="$(postgres_value 'SELECT count(*) FROM routeforge_provider_attempts;')"
./scripts/generate-demo-traffic.sh "${1:-24}"

for metric_name in \
  routeforge_requests_total \
  routeforge_provider_attempts_total \
  routeforge_tokens_total \
  routeforge_estimated_cost_micro_usd_total \
  routeforge_persistence_records_total; do
  wait_for_positive_metric "$metric_name"
done

initial_request_count="${1:-24}"
wait_for_postgres_count routeforge_requests "$((requests_before_traffic + initial_request_count))"
wait_for_postgres_count routeforge_provider_attempts "$((attempts_before_traffic + initial_request_count))"

orphaned_attempts="$(postgres_value '
  SELECT count(*)
  FROM routeforge_provider_attempts AS attempts
  LEFT JOIN routeforge_requests AS requests USING (request_id)
  WHERE requests.request_id IS NULL;
')"
if [ "$orphaned_attempts" -ne 0 ]; then
  echo "PostgreSQL contains provider attempts without parent requests" >&2
  exit 1
fi

migration_state="$(postgres_value "SELECT count(*) || ':' || max(version) FROM routeforge_schema_migrations;")"
if [ "$migration_state" != "1:1" ]; then
  echo "unexpected RouteForge migration state" >&2
  exit 1
fi
echo "PostgreSQL migration and request-attempt relationships are valid"

persisted_before_restart="$(postgres_value 'SELECT count(*) FROM routeforge_requests;')"
docker compose restart routeforge >/dev/null
wait_for_url "RouteForge after restart" "$routeforge_url/health"
persisted_after_restart="$(postgres_value 'SELECT count(*) FROM routeforge_requests;')"
if [ "$persisted_after_restart" -ne "$persisted_before_restart" ]; then
  echo "persisted request count changed across RouteForge restart" >&2
  exit 1
fi

./scripts/generate-demo-traffic.sh 2
wait_for_postgres_count routeforge_requests "$((persisted_before_restart + 2))"
if [ "$(postgres_value "SELECT count(*) || ':' || max(version) FROM routeforge_schema_migrations;")" != "1:1" ]; then
  echo "migration state changed after RouteForge restart" >&2
  exit 1
fi
wait_for_routeforge_target
wait_for_url "Grafana after RouteForge restart" "$grafana_url/api/health"
echo "PostgreSQL history survived restart and accepted new records"

rules_json="$(curl --fail --silent --show-error --max-time 5 "$prometheus_url/api/v1/rules?type=alert")"
for rule_name in \
  RouteForgeHighRequestFailureRate \
  RouteForgeHighFallbackRate \
  RouteForgeProviderTimeouts \
  RouteForgeProviderRateLimited \
  RouteForgeCircuitOpening \
  RouteForgeHighP95Latency \
  RouteForgeHighStreamingTTFC; do
  if ! printf '%s' "$rules_json" |
    jq -e --arg rule_name "$rule_name" \
      'any(.data.groups[].rules[]; .name == $rule_name)' >/dev/null; then
    echo "Prometheus did not load alert rule $rule_name" >&2
    exit 1
  fi
done
echo "Prometheus loaded all RouteForge alert rules"

wait_for_grafana_dashboard

if ! curl --fail --silent --show-error --max-time 5 \
  "$grafana_url/api/datasources/uid/routeforge-prometheus" |
  jq -e '
    .uid == "routeforge-prometheus" and
    .type == "prometheus" and
    .url == "http://prometheus:9090"
  ' >/dev/null; then
  echo "Grafana Prometheus datasource provisioning is incorrect" >&2
  exit 1
fi

if ! curl --fail --silent --show-error --max-time 10 \
  "$grafana_url/api/datasources/uid/routeforge-prometheus/health" |
  jq -e '.status == "OK"' >/dev/null; then
  echo "Grafana could not query its provisioned Prometheus datasource" >&2
  exit 1
fi
echo "Grafana datasource is provisioned and can query Prometheus"

echo "RouteForge observability stack smoke verification passed."
