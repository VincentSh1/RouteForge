# Development and validation

[Local Compose quickstart](../README.md#run-the-local-demo) ·
[Configuration](configuration.md) · [Control-plane API](control-plane.md)

## Native development

Use Go from `go.mod` and Node from `.github/workflows/go-ci.yml`; install frontend
dependencies from `web/package-lock.json`. The default standalone server requires
no external services or provider keys:

```sh
go run ./cmd/routeforge
```

It binds `127.0.0.1:8080` with mock. PostgreSQL, Redis, metrics, tracing, and the
admin listener are opt-in outside Compose. For UI development, enable PostgreSQL
and authenticated admin access, set the trusted origin to
`http://127.0.0.1:5173`, then run `npm ci` and `npm run dev` inside `web/`.
Vite proxies to the local admin listener on 8081. Do not use it as the production
Compose server. Ordinary Go/frontend unit tests need neither a database nor Redis.

## Correctness checks

From the repository root:

```sh
gofmt -w benchmarks cmd internal
go mod tidy
go vet ./...
go test -count=1 ./...
go test -race -count=1 ./...
git diff --check
```

From `web/`:

```sh
npm ci
npm run typecheck
npm test
npm run build
```

Check deterministic cold/warm replay from the root:

```sh
for state in cold warm; do
  first="$(go run ./cmd/routeforge-bench -scenario all -state "$state" -pretty=false)" || exit 1
  second="$(go run ./cmd/routeforge-bench -scenario all -state "$state" -pretty=false)" || exit 1
  test "$first" = "$second" || exit 1
done
```

Go/Console CI runs on PRs, pushes to main, and manual dispatch. The separate
[Compose smoke workflow](../.github/workflows/compose-smoke.yml) builds the real
images, starts fresh database volumes, verifies clean migrations, generates mock
traffic, and checks Redis fail-open, history/durability, authenticated control-plane
features, and monitoring. It also runs `scripts/verify-persistence.sh` against real
PostgreSQL for atomic rollback/duplicate/cancellation/NULL behavior. Cleanup runs
even on failure. Run these scripts only against a disposable mock stack: smoke
verification restarts/stops services and changes/restores runtime routing settings.
Do not run it against an existing development database you want to preserve.

Validate shell syntax with `sh -n scripts/<script>.sh`; use
`docker compose config --quiet` for Compose validation without printing resolved
secrets. CI requires a clean worktree after checks. Do not commit `web/dist`,
`node_modules`, local `.env`, test outputs, or unreviewed performance scratch reports.

## Performance is separate from correctness

The [manual performance suite](../benchmarks/performance/README.md) requires Go,
Docker Compose 2.24.4+, jq, curl, and openssl. It refuses to overwrite reports and
uses a separate disposable project. No performance number is a required PR gate.
Keep environment, workload, source provenance, warm-up, and loss coverage alongside
any claimed result; do not edit measured JSON to improve a presentation.

## Local demo troubleshooting

- **Compose rejects the missing secret:** supply a generated 32–256-byte
  `ROUTEFORGE_ADMIN_SECRET` through environment or ignored `.env`; never dump
  resolved Compose config. The Console uses that same value, not a default password.
- **Login/write returns 403:** use exactly `http://127.0.0.1:3001`, not `localhost`
  or another origin. Vite requires the explicit 5173 origin override. No CORS bypass
  is intended. Restarting RouteForge invalidates existing sessions.
- **Port already allocated:** stop the conflicting local service; do not bind the
  demo publicly as a workaround. Published ports are 8080, 8081, 3000, 3001, and 9091.
- **History is empty:** generate traffic, refresh, and inspect the persistence
  counters. Writes are asynchronous and can be dropped under pressure. A successful
  completion alone does not prove a row was committed.
- **Grafana looks empty:** open the provisioned RouteForge Overview dashboard,
  generate mock traffic, and allow multiple scrapes. No-traffic, fallback, and
  circuit-transition panels may legitimately have no observations.
- **Inspect startup safely:** `docker compose ps` and bounded service logs help;
  do not share environment dumps, session cookies, or connection URLs.

`docker compose down` retains durable local data; `docker compose down -v` removes
it. Neither command publishes a release, and no release/tag is automated here.
