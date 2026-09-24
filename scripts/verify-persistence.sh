#!/bin/sh
# Reuse the real builder/toolchain without making the production tmpfs executable.
set -eu
cd "$(dirname "$0")/.."
postgres_id=$(docker compose ps -q postgres)
test -n "$postgres_id" || { echo 'Disposable Compose PostgreSQL must be running' >&2; exit 1; }
network=$(docker inspect --format '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{end}}' "$postgres_id")
ROUTEFORGE_TEST_DATABASE_URL=$(docker compose config --format json | jq -er '.services.routeforge.environment.ROUTEFORGE_DATABASE_URL')
test -n "$ROUTEFORGE_TEST_DATABASE_URL" || { echo 'Persistence configuration is required' >&2; exit 1; }
export ROUTEFORGE_TEST_DATABASE_URL
test_container=
cleanup() {
  result=$?
  trap - EXIT
  if [ -n "$test_container" ]; then docker rm -f "$test_container" >/dev/null 2>&1 || result=1; fi
  unset ROUTEFORGE_TEST_DATABASE_URL
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT TERM
docker build --target builder -t routeforge-persistence-check:local .
test_container=$(docker create --network "$network" --user 65532:65532 \
  --read-only --tmpfs /tmp:exec,mode=1777 --cap-drop ALL \
  --security-opt no-new-privileges:true \
  -e ROUTEFORGE_TEST_DATABASE_URL -e GOCACHE=/tmp/go-build -e GOENV=off \
  routeforge-persistence-check:local \
  go test -trimpath -mod=readonly -count=1 -v -run '^TestPostgresAtomicWriteIntegration$' ./internal/persistence/postgres)
docker start -a "$test_container"
test "$(docker inspect --format '{{.State.ExitCode}}' "$test_container")" -eq 0
