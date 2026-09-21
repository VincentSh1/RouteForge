# HTTP performance measurements (v1)

These are **real local HTTP load tests**, not the deterministic routing-policy
simulator in `benchmarks/v1`. The workload is reproducible; wall-clock results
are not expected to be byte-identical. No semantic quality or real-provider
capacity is measured. There is no performance pass/fail threshold in PR CI.

## Reproduce

Prerequisites: Go from `go.mod`, Docker with Compose 2.24.4+ (`!override` support),
`jq`, and `openssl`. From the repository root:

```sh
./scripts/run-performance.sh local-run-1
```

This builds the real RouteForge Dockerfile and the Go client. It uses a separate
`routeforge-performance` project, loopback ports 18080/18090, and the existing
mock RouteForge, Redis and PostgreSQL services. It does not start Console,
Prometheus or Grafana: metrics are scraped directly outside the measured window.
Redis/PostgreSQL are private; the normal demo's containers and data are untouched.
Existing performance containers/volumes or an existing output file cause refusal.
Cleanup removes only this disposable project's containers and volumes, including
its synthetic history. Results remain in `benchmarks/performance/v1/<name>.json`.
Keep reviewed results, not every scratch run, in Git.

## Matrix and isolation

24 cases: concurrency **1, 8, 32, 64**, each with:

- synchronous unique requests → cache misses and successful writes;
- synchronous identical requests → one serial priming request, then warm hits;
- streaming requests → cache bypass.

Each runs with persistence off and on. The inference binary, telemetry and metrics
remain the same. Admin/auth and tracing are disabled in this isolated project;
the admin API requires persistence, so disabling it in both modes avoids a
configuration confound. This does not change normal Compose defaults.

Every case recreates RouteForge (empty process-local state) and flushes **only the
isolated project's Redis**. PostgreSQL remains running across cases and accumulates
history; database filesystem/OS caches are not reset. Case order is fixed: off/on,
miss/hit/stream, ascending concurrency. Repeat runs and reverse/randomized order
would be needed for stronger comparative claims; no statistical confidence is claimed.

There are 128 excluded warm-up requests (129 for warm hits, including the prime),
then up to 1,000 measured requests. Each phase stops after 30 seconds; requests
have a 5-second timeout. No interval sleeps are used to throttle the closed-loop
workers. Concurrency is a ceiling, not guaranteed simultaneous in-flight work.
Warm-up metrics/persistence outcomes settle before measurement. Failed warm-up
aborts the run. Sampling, metrics collection and persistence drain waits are
outside the reported request window.

## Report interpretation

- Throughput is terminated HTTP attempts / measured elapsed seconds; successful
  requests/sec is reported separately. Unstarted requests are not successes/errors.
- Client latency includes all measured attempts, including failures and timeouts,
  from HTTP dispatch through body validation/stream terminal marker. Synthetic
  payload construction happens before per-request timing but within phase timing.
- Nearest-rank p50/p95/p99: sort ascending, use `ceil(p * n) - 1`. Units are ms.
  Empty distributions are null. No interpolation or confidence interval is claimed.
- Streaming TTFC is the first nonempty assistant content, not headers, role-only
  chunks, pings, or total duration. It includes observed content even if the stream
  later fails; `samples` gives its denominator. Success requires content, a stop
  finish reason, and `[DONE]`. Total stream duration is separate.
- Cache-mode verification requires measured counter deltas to match the requested
  behavior, including zero actual provider attempts for warm hits. False means
  the case must not be presented as a clean cache-mode comparison.
- Persistence is asynchronous: latency includes submission overhead, **not durable
  commit latency**. `persistence_written`, `persistence_write_errors`, and
  `persistence_queue_full` are measured-phase deltas. A successful HTTP response
  may have dropped history. `persistence_settled=false` means the bounded drain
  observation did not finish; never interpret it as complete durable recording.
- Counters must come from an isolated, non-restarting server. Concurrent unrelated
  traffic invalidates the comparison. No scraping occurs during timed phases.
- Client Go/OS/architecture/CPU count and Docker VM CPU/memory/version are recorded,
  without hostname, username, paths, URLs, secrets, prompts or response content.

The host-side client and Docker VM compete for resources; container networking,
HTTP transport, client JSON/SSE parsing, OS scheduling and mock work all contribute.
The mock is tiny and immediate, so high RPS is **not LLM serving capacity**. Short
runs are sensitive to startup/scheduling noise. These are baselines, not production
capacity guarantees or evidence of a bottleneck without further profiling.

## Standalone client

```sh
go run ./cmd/routeforge-load -confirm-mock -port 18080 -metrics-port 18090 \
  -concurrency 8 -requests 1000 -warmup 128 -duration 30s \
  -cache-mode miss -persistence=false
```

The standalone tool cannot attest server configuration. `-confirm-mock` is an
explicit operator safety acknowledgment, not provider discovery. Use only an
isolated mock process: loopback alone cannot prove that a gateway has no paid
provider configured. The suite verifies its effective Compose mock setting before
sending traffic. URLs/models are not configurable, redirects and environment HTTP
proxies are disabled, and no credentials are read. Responses are bounded to 1 MiB.
Without `-metrics-port`, observations are null and cache behavior is unverified.
Counts are capped at 20,000, warm-up at 2,000, concurrency at 64, phase duration at
5 minutes and request timeout at 30 seconds. Interrupting the client cancels workers.

No production optimization is part of Phase 9A. A future optimization must follow
a measured baseline, a material bottleneck, and a comparable after measurement.

## Recorded local baseline

[Raw v1 results](v1/local-arm64-baseline.json): macOS arm64 client, 8 logical CPUs,
Go 1.26.6; Colima Linux arm64 Docker VM, 4 CPUs, approximately 5.8 GiB RAM,
Docker 29.5.2. PostgreSQL 17.11 and Redis 8.2.9; routing remained deterministic.
The source tree was dirty during this implementation run: `source_revision`
identifies its parent commit, not a claimed clean release. The checked-in load
code and method describe the workload used.

One run per case, 1,000 measured requests each, with persistence **off**:

| Concurrency | Sync miss req/s | Warm hit req/s | Warm hit p95 (ms) | Streaming p95 TTFC (ms) |
|---:|---:|---:|---:|---:|
| 1 | 1,296 | 1,981 | 0.577 | 0.756 |
| 8 | 9,729 | 10,608 | 1.135 | 1.172 |
| 32 | 15,457 | 16,097 | 3.578 | 3.011 |
| 64 | 15,858 | 20,134 | 5.080 | 5.520 |

All 24,000 measured HTTP requests succeeded; all 24 cache-mode checks passed.
However, with persistence **on**, only **5,905 of 12,000** measured records were
written; **6,095** were rejected by the bounded queue, with zero reported database
write errors. All three concurrency-1 persistence cases wrote all 1,000 records.
At concurrency 64, warm hits wrote 289 records and dropped 711; HTTP p95 was
5.848 ms versus 5.080 ms with persistence off. Streaming p95 TTFC at concurrency
64 was 8.785 ms with persistence on versus 5.520 ms off.

The observed queue saturation is a real history-throughput limit for these bursts,
not proof of a particular SQL/Go hotspot. HTTP availability and durable history
coverage are different outcomes. No code optimization was made. Some on/off
timings reverse direction at other concurrency levels: these short, single-run
measurements do not establish statistically reliable overhead ratios.

Measured windows ranged from **0.049 to 0.771 seconds**. Use these numbers as a
reproducible **short-burst baseline**, not sustained
capacity, production SLOs, or paid-provider performance. Longer repeated runs and
profiling would be needed before choosing an optimization.
