#!/bin/sh
# Runs only the private mock project. Never uses the developer's demo volumes.
set -eu
cd "$(dirname "$0")/.."
name=${1:-local}
suite=${2:-burst}
case "$suite" in burst|sustained) ;; *) echo 'Suite must be burst or sustained' >&2; exit 2;; esac
export PERF_CACHE=true
modes='miss warm_hit bypass'
concurrencies='1 8 32 64'
requests=1000
warmup=128
sampling=false
if [ "$suite" = sustained ]; then
  export PERF_CACHE=false
  modes=disabled
  concurrencies='8 32 64'
  requests=1000000
  warmup=2000
  sampling=true
fi
case "$name" in ''|*[!a-zA-Z0-9_-]*) echo 'Use a simple result name (letters, digits, underscore, hyphen)' >&2; exit 2;; esac
target="benchmarks/performance/v1/$name.json"
test ! -e "$target" || { echo 'Result already exists; choose another name' >&2; exit 1; }
for tool in docker go jq openssl curl; do command -v "$tool" >/dev/null || { echo 'Docker Compose, Go, jq, curl and openssl are required' >&2; exit 1; }; done
docker compose version >/dev/null
export ROUTEFORGE_ADMIN_SECRET="$(openssl rand -hex 32)"
export PERF_PERSISTENCE=false
compose() { docker compose -p routeforge-performance -f compose.yml -f deploy/performance/compose.yml "$@"; }
compose config --quiet
test -z "$(compose ps -aq)" || { echo 'Performance project already exists; stop it explicitly first' >&2; exit 1; }
test -z "$(docker volume ls -q --filter label=com.docker.compose.project=routeforge-performance)" || { echo 'Performance volumes already exist; inspect them before retrying' >&2; exit 1; }
scratch=$(mktemp -d)
cleanup() {
  result=$?
  trap - EXIT
  compose down -v --remove-orphans >/dev/null 2>&1 || result=1
  rm -f "$scratch/load" "$scratch/results.ndjson" "$scratch/report.json"
  rmdir "$scratch"
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT TERM
go build -trimpath -o "$scratch/load" ./cmd/routeforge-load
compose build routeforge
compose config --format json | jq -e '.services.routeforge.environment.ROUTEFORGE_PROVIDER == "mock" and .services.routeforge.environment.ROUTEFORGE_OTEL_ENABLED == "false"' >/dev/null
compose up -d --wait --wait-timeout 90 postgres redis
# Every case gets a fresh RouteForge process and empty Redis, but the same
# PostgreSQL instance. History is measured asynchronously, not as commit latency.
for persistence in false true; do
  export PERF_PERSISTENCE=$persistence
  for mode in $modes; do
    for concurrency in $concurrencies; do
      compose stop routeforge >/dev/null
      previous_rows=0
      if [ "$persistence" = true ]; then
        # The first enabled case has not applied migrations yet.
        if compose exec -T postgres psql -U routeforge_local -d routeforge -Atqc "SELECT to_regclass('public.routeforge_requests') IS NOT NULL" | grep -qx t; then
          previous_rows=$(compose exec -T postgres psql -U routeforge_local -d routeforge -Atqc 'SELECT count(*) FROM routeforge_requests')
        fi
      fi
      compose exec -T redis redis-cli FLUSHDB >/dev/null
      compose up -d --no-deps --force-recreate --wait --wait-timeout 60 routeforge >/dev/null
      # Verify effective process flags without printing environment values.
      compose exec -T routeforge sh -c 'test "$ROUTEFORGE_PROVIDER" = mock && test "$ROUTEFORGE_POSTGRES_ENABLED" = "$1" && test "$ROUTEFORGE_CACHE_ENABLED" = "$2"' sh "$persistence" "$PERF_CACHE"
      streaming=false
      if [ "$mode" = bypass ]; then streaming=true; fi
      echo "Measuring persistence=$persistence cache=$mode concurrency=$concurrency" >&2
      "$scratch/load" -confirm-mock -port 18080 -metrics-port 18090 \
        -requests "$requests" -warmup "$warmup" -concurrency "$concurrency" -duration 30s -timeout 5s \
        -sample-persistence="$sampling" -stream="$streaming" -cache-mode "$mode" -persistence="$persistence" >> "$scratch/results.ndjson"
      if [ "$persistence" = true ]; then
        written=$(curl -fsS --max-time 5 http://127.0.0.1:18090/metrics | awk '/^routeforge_persistence_records_total[{].*outcome="written"/ {sum += $NF} END {print sum+0}')
        rows=$(compose exec -T postgres psql -U routeforge_local -d routeforge -Atqc 'SELECT count(*) FROM routeforge_requests')
        test "$((rows - previous_rows))" -eq "$written" || { echo 'Durable row count differs from written counter' >&2; exit 1; }
        inconsistent=$(compose exec -T postgres psql -U routeforge_local -d routeforge -Atqc 'SELECT count(*) FROM routeforge_requests r WHERE r.attempt_count <> (SELECT count(*) FROM routeforge_provider_attempts a WHERE a.request_id=r.request_id)')
        test "$inconsistent" -eq 0 || { echo 'Request/attempt consistency failed' >&2; exit 1; }
      fi
    done
  done
done
docker_environment=$(docker info --format '{"logical_cpus":{{.NCPU}},"memory_bytes":{{.MemTotal}},"architecture":{{json .Architecture}},"os_type":{{json .OSType}},"server_version":{{json .ServerVersion}}}')
images=$(compose config --format json | jq '{postgres:.services.postgres.image,redis:.services.redis.image}')
revision=$(git rev-parse HEAD)
dirty=false
if [ -n "$(git status --porcelain)" ]; then dirty=true; fi
jq -s --arg suite "$suite" --argjson environment "$docker_environment" --argjson images "$images" \
  --arg revision "$revision" --argjson dirty "$dirty" \
  '{version:1,method:"closed_loop_mock_http",suite:$suite,source_revision:$revision,source_dirty:$dirty,docker_environment:$environment,images:$images,cases:.}' "$scratch/results.ndjson" > "$scratch/report.json"
if [ "$suite" = sustained ]; then
  jq -e 'all(.cases[]; .elapsed_seconds >= 29 and .requests < .requested_requests and .observations.persistence_settled and .observations.cache_mode_verified)' "$scratch/report.json" >/dev/null || {
    echo 'Sustained window or accounting validation failed; do not claim steady-state results' >&2
    exit 1
  }
fi
mkdir -p benchmarks/performance/v1
# Never replace an existing result, including a concurrently created one.
(set -C; jq . "$scratch/report.json" > "$target")
echo 'Performance report written; removing only the disposable performance project.' >&2
