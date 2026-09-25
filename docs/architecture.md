# Architecture and design decisions

[System diagram and quickstart](../README.md#architecture) ·
[Configuration](configuration.md) · [Control-plane API](control-plane.md)

## A modular monolith, with explicit boundaries

One Go process owns inference, routing, process-local health/accounting, and the
optional admin listeners. HTTP handlers translate wire protocols; the gateway
orchestrates provider-independent requests and typed outcomes; adapters own
upstream HTTP/SSE details. PostgreSQL and Redis adapters sit behind narrow
operational-record/cache interfaces, not inside routing policies.

This keeps request cancellation, fallback, and state ownership inspectable without
distributed coordination. Supporting services solve current needs: PostgreSQL
retains committed operational history, Redis reuses responses, Prometheus retains
time-series observations, and Grafana visualizes them. They are not reasons to
split the gateway into microservices. There is no shared routing-state store or
multi-replica coordination guarantee.

## Inference and stream commitment

A request pins one runtime routing configuration, resolves eligible candidates,
and fixes its candidate order for the request. Each selected candidate gets its
own model resolution. An eligible non-streaming cache lookup occurs before actual
circuit admission/provider invocation; a miss or cache error continues normally.

Only actual invocations create provider attempts. Retryable typed timeout,
unavailable, and rate-limit failures can advance to a fallback candidate.
Invalid requests and client cancellation must not cause indiscriminate retries.
Fallback resolves the next provider's native model from the original logical alias.

Streaming has two distinct boundaries:

- **Commitment:** the first emitted downstream SSE chunk. It can be role-only;
  once committed, fallback would mix providers into one response and is forbidden.
- **TTFC:** the first non-empty assistant content. Role-only, usage-only, heartbeat,
  and finish-only events do not qualify. TTFC can exist for a stream that later fails.

Usage-only upstream events stay internal and do not commit the response. A
post-commit failure terminates the stream without pretending successful completion.
Idle timeout and cancellation finalize attempts and history; first content does
not finalize success. Full stream lifetime is reported separately from TTFC.

## State ownership and routing

| State | Owner / lifetime | Decision role |
| --- | --- | --- |
| Provider circuits | Gateway, process-local synchronized state | Admission and one half-open probe |
| Latency/TTFC observations | Bounded process-local telemetry | Fresh mode-specific routing medians |
| Exploration counters | Gateway, separate sync/stream counters | Deterministic undersampled-provider exploration |
| Usage/configured cost | Accounting tracker, process-local | Observes actual attempts; configured pricing supports cost policies |
| Runtime routing settings | Immutable atomic snapshot | One version per request; updates affect new requests |
| PostgreSQL / Redis / OTel | Separate adapters | Never choose the provider |

Circuit failures are deliberately narrower than all unsuccessful requests.
Consecutive timeout/unavailable/rate-limit outcomes open a circuit; cancellation,
invalid requests, and internal failures do not imply a broken provider. Cooldown
allows one real half-open trial. Read-only inspection does not reserve it or mutate
state, so an OPEN circuit past cooldown can be eligible until an invocation claims
the probe. Cache hits cannot stand in for a health probe.

The four policies expose different tradeoffs rather than a fabricated universal
score. Deterministic routing preserves configured order. Latency routing needs
fresh samples for all eligible candidates and separates TTFC from completion
latency; deterministic exploration reduces cold-start selection bias. Cost routing
uses price dominance without guessing output length. Cost-latency routing bounds
measured latency relative to the fastest before applying cost preference.
Missing evidence falls back to documented stable behavior, not invented estimates.

Admin snapshots copy existing state without advancing routing or exploration.
Components are sampled independently, so a snapshot is advisory, not an atomic
promise of future admission. Runtime updates replace only policy, exploration
interval, and cost-latency tolerance. They preserve history/circuit/accounting
state and are lost on restart; concurrent saves are last-write-wins.

## Best-effort history, not a routing dependency

At terminal completion the gateway submits an immutable operational record with
its actual attempt chain to a bounded 256-record queue. One writer sends one pgx
batch per request; parent and children commit in one implicit transaction. Requests
are never combined into a cross-request transaction. SQL is parameterized and
embedded versioned migrations run under an advisory transaction lock at startup.

The queue makes database latency independent of the provider decision path. It
does not make delivery lossless: full queues drop new records; writes have bounded
timeouts and no retry loop. Runtime database errors cannot change the inference
result, provider health, or fallback. Startup fails if explicitly enabled persistence
cannot initialize. Shutdown attempts a bounded drain; crashes can lose in-flight
and queued records. Committed rows survive a gateway restart.

This is an intentional availability/history-coverage tradeoff, measured in the
[sustained load reports](../benchmarks/performance/README.md#recorded-sustained-results).
Batching reduces sequential database exchanges, but does not eliminate queue drops
at the tested closed-loop load. Increasing queue size would hide, not fix, that gap.

NULL means unavailable for TTFC, tokens, and cost; zero is a real value. Money is
integer micro-USD, not floating point. All authoritative attempt usage—including
failed/fallback attempts—can contribute. A cache-served completion creates no
new provider cost or fictitious attempt. Malformed requests rejected before the
gateway are not operational-history records.

## Cache as an optimization

Caching occurs after selection/model resolution, never instead of routing.
Canonical supported request fields plus provider and resolved model are hashed
into a versioned SHA-256 key. Values are bounded, versioned normalized successful
non-streaming completions. Expiration defaults to five minutes.

GET and SET are individually timeout-bounded (50 ms); errors fail open. Short
synchronous writes avoid another background queue/lifecycle. Streaming, errors,
and partial responses bypass storage. Concurrent misses may duplicate upstream
work: no distributed locks or singleflight cancellation coupling are introduced.
Reusing a response is explicit product behavior, not a guarantee that a provider
is deterministic. Client-visible original usage is not new operational consumption.

## Observability and the control plane serve different questions

Process-local routing telemetry is not OTel. Exporter failures and scrape schedules
cannot become routing inputs. Traces describe request/routing/actual-attempt
lifecycles, without per-chunk spans or raw payloads. Provider trace context is not
automatically propagated to third-party LLM endpoints.

Prometheus/Grafana answer historical aggregate questions using reset-aware rates,
increases, and histogram quantiles. Circuit transition counters are not current
state gauges. Configured cost can undercount missing estimates and is not invoicing.
The Console answers request-level and current-state questions through the local
admin API. It uses bounded keyset history pagination, explicit NULL displays, and
manual refresh rather than downloading all history or duplicating Grafana charts.

## Security boundaries

PostgreSQL's schema stores metadata only—never prompts, response content, headers,
raw errors, credentials, client identities, or cache values. Redis is different:
generated completions are sensitive data despite hashed key names and expiration.
There is no per-user/tenant cache separation or encryption claim. Operational model
identity is length-bounded, not a sanitizer for deliberately supplied sensitive text.

Admin authentication uses a high-entropy configured secret, constant-time digest
comparison, cryptographic session IDs, and bounded process-local sessions. Cookies
are HttpOnly, SameSite=Strict, and Secure for HTTPS origins; local HTTP is an explicit
exception. Login/logout and routing writes require the trusted Origin and JSON.
No secrets/session tokens enter browser storage; static assets remain public so
the login page can load. Session expiry is absolute, restart invalidates sessions,
and already-admitted requests may finish after logout.

The same-origin Nginx proxy allows selected admin routes; no wildcard CORS or
credential-bearing frontend configuration is needed. This is **single-admin local
security**, not an internet-ready identity/abuse system. Inference is unauthenticated,
Grafana uses local anonymous Viewer access, and Prometheus has no demo authentication.
Loopback publication, private database/cache ports, and read-only config mounts
are useful boundaries but do not replace TLS and deployment hardening for remote use.
Compose database credentials are explicitly synthetic and unsuitable for deployment.

## Two kinds of evidence

Production observations are selection-biased: the unselected provider's outcome
is unknown. Offline fixtures define every provider's counterfactual outcome for
each synthetic request. A fresh real gateway and virtual clock per policy isolate
circuits, exploration, telemetry, and accounting. Cold/warm state is explicit;
canonical results are byte-identical. Admin replay cannot mutate the live gateway.

Real HTTP load tests instead measure the running binary, transport, concurrency,
cache, and persistence on a specific machine. Warm-up is excluded and percentiles
use nearest rank. Workloads are reproducible; timings are not deterministic.
Burst and sustained runs answer different questions, and HTTP success is not proof
of durable recording. Neither tool scores semantic quality or measures commercial
provider capacity. See [benchmark fixtures](../benchmarks/README.md) and
[performance methodology](../benchmarks/performance/README.md) for reproduction.
