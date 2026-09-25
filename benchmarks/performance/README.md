# HTTP performance measurements (v1)

These are **real local HTTP load tests**, not the deterministic routing-policy
simulator in `benchmarks/v1`. The workload is reproducible; wall-clock results
are not expected to be byte-identical. No semantic quality or real-provider
capacity is measured. There is no performance pass/fail threshold in PR CI.

## Reproduce

Prerequisites: Go from `go.mod`, Docker with Compose 2.24.4+ (`!override` support),
`jq`, `curl`, and `openssl`. From the repository root:

```sh
./scripts/run-performance.sh local-run-1
```

This builds the real RouteForge Dockerfile and the Go client. It uses a separate
`routeforge-performance` project, loopback ports 18080/18090, and the existing
mock RouteForge, Redis and PostgreSQL services. It does not start Console,
Prometheus or Grafana: metrics are scraped directly. Burst mode scrapes outside
the measured window; sustained mode also samples once per second during load.
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
  traffic invalidates the comparison. Burst mode avoids scraping during timed
  phases; sustained mode includes the same one-second sampling in both builds.
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
Counts are capped at 1,000,000, warm-up at 2,000, concurrency at 64, phase duration at
5 minutes and request timeout at 30 seconds. Interrupting the client cancels workers.

The burst baseline predates the persistence optimization documented below.
Optimization requires a measured bottleneck and comparable before/after workloads.

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
coverage are different outcomes. This report predates optimization. Some on/off
timings reverse direction at other concurrency levels: these short, single-run
measurements do not establish statistically reliable overhead ratios.

Measured windows ranged from **0.049 to 0.771 seconds**. Use these numbers as a
reproducible **short-burst baseline**, not sustained
capacity, production SLOs, or paid-provider performance. The sustained experiment
below investigates the persistence bottleneck; repeated runs would still be needed
for stronger estimates of variance.

## Sustained persistence validation

```sh
./scripts/run-performance.sh sustained-run-1 sustained
```

This extends the same suite: synchronous mock requests, cache disabled, concurrency
8/32/64, persistence off/on, 2,000 excluded warm-up requests, then a **30-second**
window with a one-million-request safety cap. Samples and percentile storage are
bounded by that cap; the client may use tens of MiB for observations. Deadline
cancellation can classify up to one in-flight request per worker as a failure;
those observations are retained, not hidden as successful completions.

Unlike the original burst suite, sustained mode scrapes once per second during
the measurement window using one additional bounded client. Every comparison
uses the same sampling overhead. `persistence_samples` contains cumulative deltas
since warm-up plus instantaneous queue depth. Missing instruments are null.
Queue depth excludes the active write; one-second samples can miss shorter peaks.
Counters are individually atomic, not a transactional snapshot. For steady-state
rates, subtract samples around seconds 5 and 25 rather than including the initial
queue fill or post-load drain. Warm-up is count-based, not a claim that the database
is fully warmed; these interior intervals provide the sustained comparison.

The suite checks durable row growth against the written counter and checks each
parent's declared attempt count against its child rows outside the timed window.
The same disposable PostgreSQL instance accumulates history across the three
enabled cases; each complete suite starts with a fresh named volume. No database
port is published. All performance volumes are removed afterward.

### Persistence instruments

All new instruments are label-free and observational:

| Prometheus name | Meaning |
|---|---|
| `routeforge_persistence_submitted_total` | Submissions while accepting, including full-queue rejections |
| `routeforge_persistence_queue_depth` | Current buffered records, excluding the active write |
| `routeforge_persistence_writes_total` | Completed store write calls, successful or failed |
| `routeforge_persistence_write_duration_seconds_total` | Cumulative write-call seconds, including pool acquisition and errors, excluding queue wait |

Existing `routeforge_persistence_records_total` continues to report terminal
outcomes (`written`, `write_error`, `queue_full`). Use `rate()` on counters. Mean write-call
duration is `rate(routeforge_persistence_write_duration_seconds_total[1m]) /
rate(routeforge_persistence_writes_total[1m])` when the denominator is positive.
This is **not** PostgreSQL server-only execution time or end-to-end commit latency
from HTTP submission. The latter also includes queue wait.

### Change under test

Previously, one writer awaited BEGIN, the parent INSERT, each attempt INSERT, and
COMMIT separately. The hardened store sends one pgx batch per request. pgx executes
its statements in one implicit transaction; closing batch results consumes all
results and reports errors. Unrelated requests are never combined. There is still
one worker, a 256-record queue, a five-second write timeout, no retry loop, and
fail-open inference. Schemas, NULL semantics, and operational-only metadata are
unchanged. Shutdown still drains accepted records within its existing deadline.

The Compose correctness workflow also runs `scripts/verify-persistence.sh` to test
real PostgreSQL rollback on a late child constraint failure, duplicate rejection,
cancellation, and nullable fallback metadata. Ordinary Go tests do not require a
database. Sustained performance remains manual, not a noisy PR gate.

### Recorded sustained results

Three reports preserve the sequence: [unmodified writer](v1/sustained-baseline.json),
[instrumented original writer](v1/sustained-instrumented-baseline.json), and
[per-request batch](v1/sustained-batched.json). Same local arm64/Go 1.26.6 client,
4-CPU approximately 5.8-GiB Docker VM, PostgreSQL 17.11, and six-case workload.
Each matrix has one run per case; these are not confidence intervals or production
capacity guarantees. Dirty source revisions identify the parent commit, not a
clean release. The original writer awaited BEGIN, each INSERT and COMMIT; the
instrumented baseline added only recorder statistics, before changing that SQL path.

The initial, uninstrumented baseline dropped 91.61%, 97.40%, and 98.37% of history
at concurrency 8/32/64. The instrumented baseline showed the queue at its 256-record
bound and about 28.9 seconds spent writing during 29 sampled seconds. This is a
continuously busy serial writer, not merely a startup burst. Write time includes
transport, pool, scheduling, and PostgreSQL work; no claim is made that server-side
SQL execution alone accounts for it.

The controlled comparison below uses the **instrumented** baseline versus batch,
with identical instrumentation. Write rates and mean write times use samples near
seconds 5 and 25. Drop percentages cover the whole measured request window after
bounded drain; client p95 covers all measured HTTP attempts.

| Concurrency | Write records/s before → after | Mean write ms before → after | History dropped before → after | HTTP req/s before → after | HTTP p95 ms before → after |
|---:|---:|---:|---:|---:|---:|
| 8 | 773 → 1,337 | 1.288 → 0.739 | 91.72% → 85.92% | 9,564 → 9,630 | 1.361 → 1.309 |
| 32 | 387 → 610 | 2.575 → 1.614 | 97.39% → 95.30% | 14,586 → 13,443 | 5.010 → 5.581 |
| 64 | 279 → 487 | 3.559 → 2.003 | 98.33% → 97.03% | 17,461 → 16,360 | 8.582 → 9.093 |

Batching improved durable throughput **1.58–1.74×** in these interior windows,
demonstrating that sequential statement exchanges are a material contributor.
It is not a free inference-speed win: at concurrency 32/64, more database work
coincided with approximately 8%/6% lower HTTP throughput and 11%/6% higher p95.
The shared VM/client environment and single-run variance prevent attributing all
of that difference to one cause. With persistence disabled, the batch-build run
served 12,348 / 18,884 / 21,129 req/s with p95 0.951 / 3.332 / 6.538 ms.

The optimized enabled cases wrote 40,687 / 18,974 / 14,594 records and dropped
248,230 / 384,335 / 476,208. There were **zero database write errors**, all durable
counts and attempt-chain checks passed, and all queues settled after load stopped.
HTTP failures in all reports were only `timeout_or_cancellation` (at most one per
worker), consistent with fixed-deadline cancellation. That category does not
independently distinguish per-request timeout from phase cancellation. None were
reported as HTTP status, transport, or invalid-response errors. Full p50/p95/p99,
totals, and samples are in the JSON.

**Remaining limit:** every tested enabled case still saturates the bounded queue.
Observed capacity under competing inference load was approximately 487–1,337
records/s after batching, not a universal database capacity. A no-drop arrival-rate
threshold was not established by this closed-loop matrix. History remains best-effort,
not lossless. The smallest demonstrated improvement is retained; no larger queue,
worker pool, cross-request transaction, retry mechanism, or new infrastructure was
introduced to conceal the remaining capacity gap.
