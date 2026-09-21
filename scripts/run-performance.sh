#!/bin/sh
# Runs only the private mock project. Never uses the developer's demo volumes.
set -eu
cd "$(dirname "$0")/.."
name=${1:-local}
case "$name" in ''|*[!a-zA-Z0-9_-]*) echo 'Use a simple result name (letters, digits, underscore, hyphen)' >&2; exit 2;; esac
target="benchmarks/performance/v1/$name.json"
test ! -e "$target" || { echo 'Result already exists; choose another name' >&2; exit 1; }
for tool in docker go jq openssl; do command -v "$tool" >/dev/null || { echo 'Docker Compose, Go, jq and openssl are required' >&2; exit 1; }; done
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
  for mode in miss warm_hit bypass; do
    for concurrency in 1 8 32 64; do
      compose stop routeforge >/dev/null
      compose exec -T redis redis-cli FLUSHDB >/dev/null
      compose up -d --no-deps --force-recreate --wait --wait-timeout 60 routeforge >/dev/null
      # Verify effective process flags without printing environment values.
      compose exec -T routeforge sh -c 'test "$ROUTEFORGE_PROVIDER" = mock && test "$ROUTEFORGE_POSTGRES_ENABLED" = "$1" && test "$ROUTEFORGE_CACHE_ENABLED" = true' sh "$persistence"
      streaming=false
      if [ "$mode" = bypass ]; then streaming=true; fi
      echo "Measuring persistence=$persistence cache=$mode concurrency=$concurrency" >&2
      "$scratch/load" -confirm-mock -port 18080 -metrics-port 18090 \
        -requests 1000 -warmup 128 -concurrency "$concurrency" -duration 30s -timeout 5s \
        -stream="$streaming" -cache-mode "$mode" -persistence="$persistence" >> "$scratch/results.ndjson"
    done
  done
done
docker_environment=$(docker info --format '{"logical_cpus":{{.NCPU}},"memory_bytes":{{.MemTotal}},"architecture":{{json .Architecture}},"os_type":{{json .OSType}},"server_version":{{json .ServerVersion}}}')
images=$(compose config --format json | jq '{postgres:.services.postgres.image,redis:.services.redis.image}')
revision=$(git rev-parse HEAD)
dirty=false
if [ -n "$(git status --porcelain)" ]; then dirty=true; fi
jq -s --argjson environment "$docker_environment" --argjson images "$images" \
  --arg revision "$revision" --argjson dirty "$dirty" \
  '{version:1,method:"closed_loop_mock_http",source_revision:$revision,source_dirty:$dirty,docker_environment:$environment,images:$images,cases:.}' "$scratch/results.ndjson" > "$scratch/report.json"
mkdir -p benchmarks/performance/v1
# Never replace an existing result, including a concurrently created one.
(set -C; jq . "$scratch/report.json" > "$target")
echo 'Performance report written; removing only the disposable performance project.' >&2
