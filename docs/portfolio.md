# RouteForge: portfolio and interview brief

[Run the demo](../README.md#run-the-local-demo) · [Demo walkthrough](demo.md) ·
[Design decisions](architecture.md) · [Release notes](../CHANGELOG.md)

## Project in three sentences

RouteForge is a Go inference gateway that exposes an OpenAI-compatible chat API
across OpenAI, Anthropic, and a local mock provider. It combines routing, streaming
fallback boundaries, circuit breakers, caching, and operational history with an
authenticated React control plane. Deterministic policy experiments and measured
local HTTP load tests make its tradeoffs inspectable rather than relying on
unqualified performance claims.

## Technical highlights

- **Routing and resilience:** deterministic, latency, cost, and cost-latency
  policies; provider-specific model resolution; typed outcomes; circuit admission;
  mode-specific telemetry and deterministic exploration.
- **Truthful accounting:** actual attempts remain separate from cache hits;
  authoritative usage and integer micro-USD estimates retain unavailable values.
- **Independent supporting paths:** bounded asynchronous PostgreSQL history,
  fail-open Redis caching, OTel tracing, and low-cardinality Prometheus metrics.
  None chooses the provider for a request.
- **Operator tools:** Grafana for aggregate time series; a React/TypeScript Console
  for request/attempt history, current provider state, offline comparisons, and
  authenticated runtime routing settings.
- **Reproducible evaluation:** versioned counterfactual fixtures, isolated gateways
  and virtual clocks, bounded HTTP load tooling, and Go/frontend/Compose CI.

## Hardest engineering problems

| Problem | Implemented solution | Evidence to discuss |
| --- | --- | --- |
| Fallback without corrupting an SSE response | Allow fallback only before downstream commitment; track first content separately from terminal success | [Streaming/circuit design](architecture.md#inference-and-stream-commitment), [regression tests](../internal/gateway/service_test.go) |
| Cache hits that do not distort health or economics | Lookup after provider selection/model resolution but before real probe admission; no fabricated attempts or new token/cost accounting | [Cache design](architecture.md#cache-as-an-optimization), [cache tests](../internal/gateway/cache_test.go) |
| Healthy inference hiding lost history | Observe bounded queue saturation, measure sustained writer capacity, then batch each request's SQL without weakening its transaction | [Before/after evidence](../benchmarks/performance/README.md#recorded-sustained-results), [transaction tests](../internal/persistence/postgres/integration_test.go) |
| Runtime controls without mixed settings inside a request | Atomically replace immutable settings; each request pins one version while retaining telemetry, circuits, and accounting | [Runtime settings](control-plane.md#runtime-routing-settings), [concurrency tests](../internal/gateway/runtime_routing_test.go) |

## Tradeoffs worth explaining

- A modular monolith keeps cancellation, state ownership, and fallback understandable;
  there is no distributed routing-state coordination promise.
- Bounded fail-open persistence protects inference availability, but queue overflow
  and process failure can lose history. Committed PostgreSQL rows are durable;
  submission to an in-memory queue is not.
- Cache reuse is intentional behavior, not a claim of deterministic model output.
  Streaming bypasses it, and concurrent misses may invoke a provider more than once.
- Grafana and the Console answer different questions. Current snapshots are not
  historical analytics or guarantees of future circuit admission.
- Offline fixtures compare policies under identical counterfactual conditions;
  real HTTP load tests measure local transport/concurrency/storage overhead.
  Neither evaluates semantic response quality.

## Defensible measured results

The [raw reports and methodology](../benchmarks/performance/README.md) describe a
macOS arm64 Go 1.26.6 client (8 logical CPUs), a Colima Linux arm64 Docker VM
(4 CPUs, about 5.8 GiB), PostgreSQL 17.11, and mock responses—not paid providers.

- Short bursts: 24,000 measured HTTP requests succeeded, but the 12,000
  persistence-enabled requests lost 6,095 history records. Windows were only
  0.049–0.771 seconds; these are not sustained capacity results.
- Sustained comparison: cache disabled, concurrency 8/32/64, 2,000 excluded warm-up
  requests, 30-second runs. Per-request pgx batching achieved **1.58–1.74× higher
  persistence throughput** than the identically instrumented sequential writer,
  using samples near seconds 5–25.
- The optimized runs still dropped **85.92–97.03%** of history at those offered
  loads. At concurrency 32/64, they coincided with roughly 8%/6% less HTTP throughput
  and 11%/6% higher p95. This improved one bottleneck; it did not make history lossless.

One run per case, shared host resources, and dirty-source provenance limit the
claim. No production RPS, cost savings, confidence interval, or no-drop threshold
has been established. Keep these qualifications when presenting the improvement.

## Security and limitations

PostgreSQL stores operational metadata, not prompts, responses, credentials, or
raw provider errors. Redis values contain generated responses and remain sensitive
despite hashed keys and TTL. Metrics use bounded operational labels. The Console
uses expiring HttpOnly sessions, trusted-Origin checks for writes, and no credential
storage in localStorage/sessionStorage.

This is a local single-admin control plane, not a multi-tenant production service.
Inference remains unauthenticated; remote use needs TLS and additional boundary
security. Sessions and runtime settings reset on restart. History is best-effort,
cache stampedes are not prevented, and configured costs are not invoices.
See the [full boundaries](architecture.md#security-boundaries).

## Resume bullets

- Built a Go inference gateway with OpenAI/Anthropic adapters, four routing policies,
  SSE streaming, typed fallback, and circuit breakers; validated policy behavior
  with isolated deterministic benchmarks and Docker integration tests.
- Improved PostgreSQL history-write throughput **1.58–1.74× in local sustained-load
  tests** by batching each request's inserts, preserving transactional consistency
  and bounded, fail-open inference while measuring remaining queue drops.
- Built an authenticated React/TypeScript operations console with request-attempt
  history and atomic runtime routing controls, alongside OTel tracing and
  Prometheus/Grafana monitoring with explicit metadata-only history boundaries.

Use only claims you can explain from the code and reports. These bullets do not
claim production adoption, commercial savings, or semantic-quality improvements.

## 30-second explanation

“RouteForge is a Go gateway for chat-completion providers. I built routing,
streaming fallback, circuit breakers, and a local React console so I could inspect
each request's actual provider attempts. I also separated deterministic routing
experiments from real HTTP load tests. Those tests exposed a persistence bottleneck;
batching improved local write throughput 1.58–1.74×, but the bounded queue still
drops history under heavy load. That availability-versus-history tradeoff is explicit.”

## Two-minute explanation

“I built RouteForge to explore the infrastructure around serving multiple LLM
providers, rather than just wrapping one API. The Go gateway exposes a compatible
chat endpoint and supports deterministic, latency, cost, and cost-latency routing.
Provider adapters share typed outcomes, so fallback and circuits don't depend on
parsing arbitrary error strings.

One difficult boundary was streaming. Before the first emitted SSE chunk, an
eligible failure can fall back. After commitment, switching providers would mix
responses, so the stream ends honestly. First content and full stream duration
are separate measurements.

Another was keeping supporting services outside routing decisions. Redis is checked
after selection and fails open. A cache hit doesn't create a provider attempt or
new provider usage. PostgreSQL receives completed metadata through a bounded queue,
not prompts or responses. The local authenticated Console reads that history and
current gateway state; Grafana handles aggregate time series.

For evaluation, the offline benchmark gives every policy identical simulated
provider outcomes in an isolated gateway. The HTTP load tool instead measures the
running mock stack. Burst tests showed successful HTTP responses could hide dropped
history. I then ran sustained tests and found a continuously busy serial writer.
Per-request pgx batching improved write throughput 1.58–1.74× in that local comparison,
without increasing the queue or changing transaction semantics.

That is not a production-capacity claim. The tested loads still saturated history,
and heavier database work coincided with worse inference p95 at higher concurrency.
The project demonstrates measured tradeoffs, reproducible tests, and explicit
failure boundaries—not lossless auditing or semantic-quality routing.”
