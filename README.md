# RouteForge

RouteForge is an OpenAI-compatible AI inference gateway written in Go. It
supports streaming and synchronous chat completions through mock, OpenAI, and
Anthropic providers with explicit selection, fallback, and optional latency-,
cost-, or latency-constrained cost routing.

## API

- `GET /health`
- `POST /v1/chat/completions`
- `system`, `user`, and `assistant` message roles
- Streaming and non-streaming chat completions through provider interfaces
- OpenAI-style success and error responses

Authentication, rate limiting, and semantic-quality routing
are intentionally out of scope. A local read-only React console, optional
OpenTelemetry tracing, Prometheus metrics, and a provisionable Grafana
operations dashboard are documented below.

## Requirements

- Go 1.26 or newer

## Run

```sh
go run ./cmd/routeforge
```

The server listens on `127.0.0.1:8080` by default. Configuration uses
environment variables:

| Variable | Default |
| --- | --- |
| `ROUTEFORGE_ADDR` | `127.0.0.1:8080` |
| `ROUTEFORGE_READ_TIMEOUT` | `15s` |
| `ROUTEFORGE_WRITE_TIMEOUT` | `30s` |
| `ROUTEFORGE_IDLE_TIMEOUT` | `60s` |
| `ROUTEFORGE_SHUTDOWN_TIMEOUT` | `10s` |
| `ROUTEFORGE_PROVIDER_TIMEOUT` | `30s` |
| `ROUTEFORGE_STREAM_IDLE_TIMEOUT` | `30s` |
| `ROUTEFORGE_CIRCUIT_FAILURE_THRESHOLD` | `3` |
| `ROUTEFORGE_CIRCUIT_OPEN_DURATION` | `30s` |
| `ROUTEFORGE_ROUTING_POLICY` | `deterministic` |
| `ROUTEFORGE_ROUTING_MIN_SAMPLES` | `5` |
| `ROUTEFORGE_ROUTING_SAMPLE_MAX_AGE` | `5m` |
| `ROUTEFORGE_ROUTING_EXPLORATION_INTERVAL` | `10` |
| `ROUTEFORGE_ROUTING_MAX_LATENCY_OVER_FASTEST_PERCENT` | unset; required for `cost_latency` |
| `ROUTEFORGE_OTEL_ENABLED` | `false` |
| `ROUTEFORGE_OTEL_EXPORTER_OTLP_ENDPOINT` | unset; required when OpenTelemetry is enabled |
| `ROUTEFORGE_METRICS_ENABLED` | `false` |
| `ROUTEFORGE_METRICS_ADDR` | `127.0.0.1:9090` |
| `ROUTEFORGE_PROVIDER` | `mock` |
| `OPENAI_API_KEY` | unset |
| `ANTHROPIC_API_KEY` | unset |
| `ROUTEFORGE_MODEL_GENERAL_OPENAI` | unset |
| `ROUTEFORGE_MODEL_GENERAL_ANTHROPIC` | unset |
| `ROUTEFORGE_PRICE_OPENAI_INPUT_USD_PER_MILLION` | unset |
| `ROUTEFORGE_PRICE_OPENAI_OUTPUT_USD_PER_MILLION` | unset |
| `ROUTEFORGE_PRICE_ANTHROPIC_INPUT_USD_PER_MILLION` | unset |
| `ROUTEFORGE_PRICE_ANTHROPIC_OUTPUT_USD_PER_MILLION` | unset |
| `ROUTEFORGE_PRICE_MOCK_INPUT_USD_PER_MILLION` | unset |
| `ROUTEFORGE_PRICE_MOCK_OUTPUT_USD_PER_MILLION` | unset |

## Example

```sh
curl http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "mock-model",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

Omitting `stream` is equivalent to setting it to `false`. Unknown JSON fields
are ignored for client compatibility. The mock provider reports deterministic
synthetic usage of three input and four output tokens; it does not tokenize the
request.

## Streaming

Set `stream` to `true` to receive OpenAI-compatible Server-Sent Events. The
default mock provider requires no credentials:

```sh
curl -N http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "routeforge/general",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'
```

The output contains incremental chunks and a completion marker:

```text
data: {..."content":"Hello"...}

data: {..."content":" from"...}

data: {..."content":" RouteForge."...}

data: [DONE]
```

RouteForge may try another provider only before any stream content has been
emitted to the client. Once the first SSE chunk is emitted, provider selection
is committed for that response. A later provider failure terminates the stream
without fallback or an error event.

`ROUTEFORGE_PROVIDER_TIMEOUT` continues to bound synchronous provider calls.
Streaming calls are not limited to that total duration. Instead,
`ROUTEFORGE_STREAM_IDLE_TIMEOUT` cancels an upstream stream when no response
data arrives for the configured duration; each read of upstream stream data
resets the inactivity timer.

## Provider selection

`ROUTEFORGE_PROVIDER` accepts `mock`, `openai`, `anthropic`, or `auto`.
Selecting a real provider requires its corresponding API key. By default,
`auto` uses each configured real provider at most once, in this fixed order:

1. OpenAI
2. Anthropic

Auto mode falls back only after rate limiting, timeout, or temporary
unavailability. It does not fall back after an invalid request, and it never
falls back to the mock provider.

### Routing policies

`ROUTEFORGE_ROUTING_POLICY=deterministic` is the default and preserves the
configured provider order exactly. `latency`, `cost`, and `cost_latency` are
opt-in policies for auto mode. Explicit provider selection is never redirected
by a routing policy.

The latency policy uses the median of recent synchronous completion latency for
non-streaming requests and recent time to first assistant content for streaming
requests. It does not mix those samples or use total stream duration. Latency
comparisons require every circuit-eligible candidate to have at least
`ROUTEFORGE_ROUTING_MIN_SAMPLES` samples no older than
`ROUTEFORGE_ROUTING_SAMPLE_MAX_AGE`.

While an eligible provider lacks those samples, RouteForge deterministically
uses every `ROUTEFORGE_ROUTING_EXPLORATION_INTERVAL`th under-sampled routing
decision for warm-up. The provider with the largest fresh-sample deficit moves
to the front for that request; configured order breaks equal deficits. Other
requests keep Phase 4C ordering. Non-streaming and streaming warm-up use
separate counters and their respective latency or TTFC samples. This is
deterministic exploration, not random or bandit routing.

Circuit-open providers are excluded before exploration. Circuit inspection
does not reserve a HALF_OPEN trial; the normal atomic admission check remains
authoritative immediately before an attempt. Exploration stops affecting order
as soon as every eligible provider has enough fresh samples, and it may resume
when samples expire.

To avoid switching on minor noise, the fastest candidate must be at least 10%
faster than the deterministic-first candidate. When it is, RouteForge moves
only that candidate to the front and preserves the relative fallback order of
the remaining providers. Circuit eligibility is evaluated first, and atomic
HALF_OPEN admission remains authoritative. The ordered candidate list is
calculated once per request and is not reranked during fallback.

Exploration counters and telemetry are process-local, bounded, and reset when
RouteForge restarts. Exploration applies only to latency-policy auto routing;
deterministic and explicit-provider modes are unchanged.

The cost policy compares the configured input and output rates for each
candidate's resolved provider-native model. RouteForge does not inspect prompt
text, estimate tokens, or use historical request totals while making this
decision. Provider A can move ahead of Provider B only when A's input rate and
output rate are both no greater than B's and at least one rate is strictly
lower. For example, fictional rates of `2` input and `8` output dominate rates
of `3` input and `12` output.

When one provider has cheaper input but more expensive output, neither provider
dominates; configured order is preserved because RouteForge does not invent an
input/output weighting. Identical rates, incomplete rates, and missing pricing
also preserve stable configured preference. Missing pricing never means free
and never excludes a provider from fallback. Circuit eligibility is applied
before cost ordering, and the resulting candidate order remains fixed for the
request. Cost routing requires no latency warm-up and does not use latency
exploration.

The `cost_latency` policy is latency-constrained cost routing. It first removes
circuit-ineligible providers and requires every remaining candidate to have
enough fresh, mode-specific samples. Until then, it reuses the deterministic
warm-up behavior described above and does not rank by price. Once the candidate
set is sufficiently measured, RouteForge finds the fastest median and divides
providers using the operator-supplied tolerance:

```sh
export ROUTEFORGE_ROUTING_POLICY="cost_latency"
export ROUTEFORGE_ROUTING_MAX_LATENCY_OVER_FASTEST_PERCENT="20"
```

The example permits providers whose median is at most 20% slower than the
fastest measured provider. The percentage is operator policy, not a universal
research-derived constant; RouteForge supplies no default. Zero is valid and
admits only providers tied with the fastest median. Synchronous requests use
completion latency, while streaming requests use TTFC and never total stream
duration.

Only providers inside the acceptable-latency partition are reordered using the
same input/output price-dominance rule as `cost`. Missing, identical, or
conflicting rates preserve configured order. Providers outside the partition
remain behind all acceptable providers, regardless of price, but remain
available for normal transient fallback. Pricing uses each candidate's resolved
provider-native model. The policy does not estimate prompt tokens, combine
milliseconds and money in a weighted score, or use a semantic quality signal.
The existing latency policy's 10% switching margin is separate: it suppresses
minor latency noise, whereas this setting expresses tolerated slowdown for an
economic preference.

### Research background

[FrugalGPT](https://arxiv.org/abs/2305.05176) motivates cost-aware selection
across models with heterogeneous economics, and
[RouterBench](https://arxiv.org/abs/2403.12031) motivates treating routing as an
explicit tradeoff rather than hiding it in an arbitrary scalar score.
[RouteLLM](https://arxiv.org/abs/2406.18665) and
[Hybrid LLM](https://arxiv.org/abs/2404.14618) study quality- or
difficulty-aware routing capabilities that RouteForge does not currently have.
`cost_latency` is a narrow systems policy over measured latency/TTFC and
operator-configured rates; it does not reproduce those research systems, make
semantic-quality claims, or imply endorsement by their authors.

## Passive provider health

RouteForge tracks short-term provider reliability in memory. Timeouts,
temporary unavailability, upstream 5xx responses, and rate limiting count
toward `ROUTEFORGE_CIRCUIT_FAILURE_THRESHOLD`. Invalid requests, model
resolution errors, adapter-internal errors, and client cancellation do not.

A provider circuit begins `CLOSED`. Reaching the failure threshold moves it to
`OPEN` for `ROUTEFORGE_CIRCUIT_OPEN_DURATION`. After that cooldown, one request
is admitted as a `HALF_OPEN` trial. A successful completion closes the circuit;
a relevant failure opens it again. Concurrent requests cannot all become
half-open trials.

Auto routing skips open providers while retaining its configured provider
order. Explicit selection never falls back to another provider: it fails fast
while the selected provider is open, although an explicit request may claim
the available half-open trial after cooldown. Streaming success is recorded
only after the provider's normal completion marker. Failures after stream
commitment still affect health but never trigger fallback for that response.

Health state is local to the RouteForge process and resets on restart. There
are no background probes or distributed circuit coordination in this phase.

## Passive provider telemetry

Every actual provider attempt records process-local operational metadata. The
gateway tracks attempts, successes, cancellations, typed failure counts,
recent timestamps, and bounded rolling latency samples independently for each
provider. Circuit-skipped providers and providers without a usable model
mapping do not record attempts.

For synchronous calls, latency runs from provider invocation until the
provider returns a response or error. For streaming calls, time to first
content runs from provider invocation until the first provider-independent
chunk with non-empty assistant content. Role-only chunks, finish-only chunks,
and provider heartbeat/ping events do not count as first content. Total stream
duration runs until normal provider completion, failure, or cancellation.
Failed streams retain their elapsed duration and any previously observed time
to first content, but are never counted as successes.

Telemetry uses fixed-size rolling samples rather than retaining an unbounded
request history. Latency samples include observation times so stale values can
be excluded from opt-in latency routing. It contains no prompts, message
content, bodies, credentials, headers, or raw provider errors. Measurements
reset when the process restarts. The default routing policy remains
deterministic; no telemetry endpoint is provided.

## OpenTelemetry tracing

OpenTelemetry tracing is optional and disabled by default. Disabled mode does
not initialize an exporter, open an observability network connection, or alter
routing. RouteForge's bounded process-local routing telemetry, circuit state,
and accounting remain separate from exported traces.

Enable the vendor-neutral OTLP/HTTP trace exporter with an explicit collector
endpoint:

```sh
export ROUTEFORGE_OTEL_ENABLED="true"
export ROUTEFORGE_OTEL_EXPORTER_OTLP_ENDPOINT="https://collector.example/v1/traces"
go run ./cmd/routeforge
```

If the configured URL has no path, RouteForge appends `/v1/traces`. HTTPS is
recommended for remote collectors. Plain HTTP can be selected explicitly for
a trusted local collector, for example
`http://127.0.0.1:4318/v1/traces`; RouteForge never silently changes HTTPS to
HTTP.

The trace hierarchy is intentionally small:

```text
routeforge.request
├─ routeforge.routing
├─ routeforge.provider.attempt (OpenAI timeout)
└─ routeforge.provider.attempt (Anthropic success, fallback=true)
```

Request spans cover `POST /v1/chat/completions`, routing spans describe the
bounded policy decision, and provider-attempt spans represent only actual
provider invocations. Streaming attempts remain open until normal completion,
failure, or cancellation and emit one `routeforge.first_content` event; chunks
do not create spans. Circuit skips are events, and half-open trial admission is
an attempt attribute where available.

RouteForge accepts incoming W3C `traceparent` context. It intentionally does
not inject RouteForge trace headers into OpenAI or Anthropic requests in this
phase. Traces contain operational categories and bounded identifiers only.
Prompts, responses, request/response bodies, API keys, authorization headers,
cookies, raw provider errors, and user identifiers are never attached. The
configured resolved model is recorded only for a bounded logical-model mapping;
arbitrary client-supplied provider-native model names are omitted.

## OpenTelemetry metrics and Prometheus

Operational metrics are optional and independently configurable from tracing.
They observe the request and provider lifecycle but never replace or feed
RouteForge's process-local routing telemetry, accounting, or circuit state.
Provider selection behaves identically whether metrics are enabled, disabled,
scraped, or never scraped.

Enable the dedicated Prometheus listener with:

```sh
export ROUTEFORGE_METRICS_ENABLED="true"
export ROUTEFORGE_METRICS_ADDR="127.0.0.1:9090"
go run ./cmd/routeforge
```

Then scrape it locally:

```sh
curl http://127.0.0.1:9090/metrics
```

The metrics listener is separate from the OpenAI-compatible API listener and
defaults to loopback. No authentication is added in this phase. Operators who
explicitly bind it beyond loopback must protect it with deployment and network
controls.

| Metric | Type | Meaning |
| --- | --- | --- |
| `routeforge_requests_total` | counter | Chat-completion requests by policy, streaming mode, and bounded outcome |
| `routeforge_request_duration_seconds` | histogram | Full downstream request lifetime, including stream lifetime |
| `routeforge_routing_selections_total` | counter | Initial provider actually selected |
| `routeforge_provider_attempts_total` | counter | Actual provider attempts by bounded outcome and fallback status |
| `routeforge_provider_duration_seconds` | histogram | Full provider invocation or upstream stream lifetime |
| `routeforge_provider_ttfc_seconds` | histogram | Time to first non-empty assistant content |
| `routeforge_fallbacks_total` | counter | Actual transitions to a subsequent provider attempt |
| `routeforge_circuit_transitions_total` | counter | Authoritative `closed`, `open`, and `half_open` transitions |
| `routeforge_tokens_total` | counter | Authoritative provider-reported input/output tokens |
| `routeforge_estimated_cost_micro_usd_total` | counter | Existing configured cost estimates in integer micro-USD |
| `routeforge_persistence_records_total` | counter | Durable history records written, dropped because the queue was full, or rejected by a runtime write error |

For example, a scrape may contain:

```text
routeforge_requests_total{outcome="success",routing_policy="deterministic",streaming="false"} 1
routeforge_provider_attempts_total{fallback="false",outcome="success",provider="mock",streaming="false"} 1
routeforge_tokens_total{direction="input",provider="mock"} 8
```

Metrics use only bounded labels: configured provider name, routing policy,
streaming/fallback booleans, typed outcome, circuit state, and token direction.
Arbitrary model names, prompts, responses, bodies, credentials, authorization
headers, user/request/trace identifiers, URLs, raw errors, and filesystem paths
are not labels. Missing usage or pricing produces no fabricated token or
zero-cost observation. The exporter uses a private Prometheus registry, so the
endpoint does not automatically publish Go runtime or process collectors.

Tracing and metrics support all four combinations independently: both off,
tracing only, metrics only, or both on. Metrics disabled starts no Prometheus
exporter or listener; tracing disabled still requires no OTLP endpoint.

## Observability dashboard

Phase 6C provides a curated, provisionable local monitoring path without
changing RouteForge routing or metric instrumentation:

```text
RouteForge (127.0.0.1:9090/metrics)
    -> Prometheus (127.0.0.1:9091)
    -> Grafana (RouteForge Overview)
```

Start RouteForge with its loopback metrics listener as shown above. From the
repository root, a locally installed Prometheus can load the checked-in scrape
and alert configuration while using port 9091 for its own UI (RouteForge
already uses 9090):

```sh
prometheus \
  --config.file=deploy/observability/prometheus/prometheus.yml \
  --web.listen-address=127.0.0.1:9091
```

The Prometheus configuration uses a 15-second scrape/evaluation interval and
loads [the RouteForge alert rules](deploy/observability/prometheus/alerts.yml).
It has no remote write or external service configuration.

Grafana provisioning lives under
`deploy/observability/grafana/provisioning`. Point a local Grafana installation's
provisioning directory there while its working directory is the repository
root, or copy the provisioning tree for that installation. For a native local
setup, provide the deployment-specific values before starting Grafana:

```sh
export GRAFANA_PROMETHEUS_URL="http://127.0.0.1:9091"
export GRAFANA_DASHBOARDS_PATH="deploy/observability/grafana/dashboards"
```

The provisioned Prometheus datasource has the stable UID
`routeforge-prometheus`. The checked-in
[RouteForge Overview dashboard](deploy/observability/grafana/dashboards/routeforge-overview.json)
contains these sections:

- Overview: request rate, success rate, all-traffic fallback rate, range token
  totals, and range estimated configured cost.
- Request performance: p50 and p95 complete RouteForge request duration,
  distinguishing streaming and non-streaming traffic.
- Provider performance: p50/p95 provider lifetime and separate p50/p95
  streaming TTFC.
- Routing: initial selections, selection share, actual attempts, and traffic by
  policy.
- Failures and fallback: typed provider outcomes and bounded fallback paths.
- Circuit breaker: transition rates and range counts, without pretending to
  expose authoritative current state.
- Tokens and estimated cost: consumption rates/range totals and configured-cost
  estimates by provider.

Dashboard totals use `increase()`, rates use `rate()`, and latency panels use
`histogram_quantile()` over histogram buckets so RouteForge counter resets are
handled correctly. The fallback-rate denominator is all RouteForge request
activity because fallback metrics do not carry routing-policy or streaming
labels. Estimated micro-USD is converted to USD for display; it is not an
invoice amount and can underrepresent attempts where usage or pricing was
unavailable.

[Example operational SLOs](deploy/observability/SLOS.md) define adjustable
local targets for 7-day request success, non-streaming p95 request duration,
and streaming p95 TTFC. They deliberately define neither cost nor semantic
quality objectives. Alerts cover sustained request failures, elevated
fallback, repeated provider timeout/rate-limit outcomes, circuit openings, and
high request latency/TTFC. Their severity labels express relative urgency only
and do not establish a paging policy.

The dashboard variables are restricted to bounded `provider`,
`routing_policy`, and `streaming` labels. No prompt, response, arbitrary model,
request/user identifier, raw error, credential, or filesystem value is queried
or displayed. The metrics endpoint remains loopback-only by default; protect it
with deployment/network controls before intentionally binding it beyond
loopback.

## Local observability demo

The repository includes a minimal Compose stack for a reproducible local
RouteForge, Redis, PostgreSQL, Prometheus, and Grafana demo. Docker with Compose is
the only runtime prerequisite; the traffic script runs its HTTP client inside
the RouteForge container.

Set `ROUTEFORGE_ADMIN_SECRET` to a generated 32–256-byte secret from your
password manager, using your environment or an ignored `.env` file. Use that
same value at the Console Login screen. No admin credential is checked in.
Then start the stack from the repository root:

```sh
docker compose up --build -d
```

Generate 40 bounded mock requests, split evenly between synchronous and
streaming calls:

```sh
./scripts/generate-demo-traffic.sh
```

An optional count from 1 through 200 is accepted:

```sh
./scripts/generate-demo-traffic.sh 100
```

Open the automatically provisioned **RouteForge Overview** dashboard at
<http://127.0.0.1:3000>. Grafana uses anonymous Viewer access for this
loopback-only development stack, so no repository credential is required.
Optional Prometheus debugging is available at <http://127.0.0.1:9091>.

Stop the services while retaining local PostgreSQL, Prometheus, and Grafana data:

```sh
docker compose down
```

To deliberately remove the named data volumes as well:

```sh
docker compose down -v
```

The stack uses the mock provider, enables Redis caching, PostgreSQL persistence,
the read-only admin API, and metrics,
and leaves OTLP tracing disabled. RouteForge binds `0.0.0.0` only inside its
container so Prometheus can scrape `routeforge:9090` over the private Compose
network. The host inference/admin API, Prometheus UI, and Grafana ports are restricted to
`127.0.0.1`; neither the RouteForge metrics port, Redis, nor PostgreSQL is published to
the host.

Mock prices of 2 fictional USD per million input tokens and 8 fictional USD per
million output tokens are configured solely to populate estimated-cost panels.
They are synthetic demo pricing, not commercial pricing or an invoice estimate.
The stack pins Go 1.26.6 (Alpine 3.23 builder), Alpine 3.21.6 runtime,
Redis 8.2.9-alpine3.22, PostgreSQL 17.11-bookworm, Prometheus 3.13.2, and Grafana 13.2.1
instead of using floating image tags. Prometheus and Grafana runtime data live
in named volumes, while scrape, alert, datasource, and dashboard configuration
is mounted read-only.

Anonymous Grafana access is appropriate only because port 3000 is bound to
loopback for this local demo. Do not expose this configuration outside the
local machine; enable authentication and deployment-specific network controls
for any shared environment. Successful mock traffic fills traffic, latency,
TTFC, token, and estimated-cost panels. Fallback and circuit panels may remain
empty because the demo does not fabricate provider failures.

The Compose stack is also exercised by a focused GitHub Actions smoke test.
It builds the RouteForge and console images, starts all six services, generates bounded
mock traffic, verifies durable request and attempt rows across a RouteForge
restart, verifies Prometheus scraping, metrics, and alert rules, confirms
Grafana dashboard and datasource provisioning, and always removes the CI
containers and volumes afterward. The workflow uses no provider credentials.
It also verifies cache miss/write/hit behavior, absence of provider attempts
on hits, PostgreSQL cache metadata, reuse across a RouteForge restart, and
successful inference while Redis is stopped followed by cache recovery.

## Optional Redis response caching

```sh
export ROUTEFORGE_CACHE_ENABLED="true"
export ROUTEFORGE_REDIS_URL="redis://127.0.0.1:6379"
export ROUTEFORGE_CACHE_TTL="5m"
```

Caching is disabled by default; disabled mode creates no Redis client. Enabled
mode requires a valid `redis://` or `rediss://` URL (optional database path,
no query options). Use authenticated `rediss://` with verified TLS outside a
trusted local network. The client is go-redis v9.22.0. Malformed configuration
fails startup; temporary Redis unavailability does not prevent startup or
inference. TTL must be at least 1ms and defaults to five minutes.

Normal routing selects an eligible provider and resolves its native model
before cache lookup. Streaming bypasses caching entirely. Only valid
non-streaming requests and successful normal assistant completions with a
`stop` finish reason are cached; failures and truncated/partial responses are
excluded. The supported request fields are model, ordered role/content
messages, and streaming mode; no temperature or other randomness controls are
currently supported. Enabling caching intentionally reuses previous answers
for identical eligible requests, even if an uncached provider might generate
a different answer.

Keys use `routeforge:chat:v1:` followed by a fixed-size SHA-256 hex digest of
the complete supported request, provider, and resolved model. Key names contain
no raw messages. The versioned JSON value stores only the normalized completion
and is limited to 1 MiB. Reads use bounded Redis GETRANGE to reject oversized
values without retrieving them in full. Unknown/malformed values fail open.
No request body, cache key, hash, or cached text enters PostgreSQL, logs, metrics,
or traces. Redis values **do contain generated response content** and must be
treated as sensitive transient data. Identical requests share entries across
callers; this phase has no user or tenant isolation.

GET and SET operations each have a fixed 50ms timeout; command retries are
disabled and connection dialing gets one attempt. SET is synchronous and
tightly bounded, adding at most one operation budget after a successful
provider call. There is no write queue or goroutine per request. Lookup errors
fall through to normal provider admission; write errors preserve the successful
response. A cache hit never reserves a half-open trial or updates provider
health, latency, token usage, or estimated cost. Its client-visible usage and
completion ID/timestamp describe the original generation. Routing selection
metrics still count the selected provider; no provider attempt span is created.

PostgreSQL migration 0002 adds only `cache_hit`. A hit records a successful
request and the provider whose entry was selected, with no fabricated attempt.
If an earlier actual attempt failed, that attempt remains in the chain.
Attempt/fallback counters continue counting actual upstream invocations; a
cache-served fallback does not increment the existing provider-fallback metric.
`routeforge_cache_lookups_total{result="hit|miss|error"}` and
`routeforge_cache_writes_total{result="success|error"}` report cache operations
without provider/model/key labels. Traces use only a bounded `cache.result`
event. No saved-cost or semantic-quality estimates are calculated.

Compose Redis is private, uses a 64 MiB `allkeys-lru` cache, and disables AOF/RDB
persistence with a tmpfs data directory and no data volume. Entries may survive
a RouteForge-only restart, but are lost when Redis restarts. PostgreSQL remains
the durable operational history store. Expiration and the version prefix are
the only invalidation mechanisms; concurrent misses may invoke a provider more
than once. There are no distributed locks, singleflight, invalidation APIs,
or background scans. Offline benchmark runs never initialize Redis.

## PostgreSQL operational history

Durable operational history is optional outside Compose:

The PostgreSQL adapter uses pgx v5.9.0 with the Go 1.26 toolchain.

```sh
export ROUTEFORGE_POSTGRES_ENABLED="true"
export ROUTEFORGE_DATABASE_URL="postgres://routeforge:local-only@127.0.0.1:5432/routeforge"
```

When enabled, RouteForge validates connectivity, applies embedded versioned
migrations, and queues terminal request records for a bounded background
writer. Startup fails if the explicitly configured database is unavailable,
but a runtime database write failure never changes inference, routing,
fallback, circuit, telemetry, or accounting outcomes.

History contains an opaque RouteForge request ID, timestamps, routing policy,
streaming mode, bounded model identity, the actual provider-attempt chain,
typed outcomes, durations and TTFC, authoritative nullable token usage, and
nullable estimated micro-USD cost. It never contains prompts, messages,
responses, request or response bodies, credentials, user identifiers, client
addresses, trace IDs, or raw provider errors.

The Compose database uses a private network, a named `postgres-data` volume,
and clearly synthetic local-only credentials. History survives a RouteForge
container restart. `docker compose down -v` deliberately deletes it along with
the other local observability volumes. The optional read-only API below exposes
this operational metadata on a separate local listener.

One background writer consumes a queue of at most 256 completed requests.
Submission never waits for database I/O; full queues drop the new record.
Writes have a five-second timeout and no automatic retries. Shutdown stops
submission and drains until its deadline, then cancels outstanding work.
`routeforge_persistence_records_total` reports `written`, `write_error`, and
`queue_full`; abandoned shutdown records count as write errors. A crash can
lose in-flight requests and queued records. This is best-effort operational
history, not an audit log with guaranteed delivery.

Records cover gateway operations; malformed HTTP payloads rejected before
entering the gateway are excluded. Streaming records finalize at stream
termination, retaining usage and TTFC observed before any later failure.
`final_provider` is NULL when no provider completes successfully. Model
identities are limited to 256 characters. Durations are integer microseconds,
timestamps are UTC, and monetary values are integer micro-USD; values exceeding
PostgreSQL BIGINT are rejected observably rather than rounded or wrapped.
The schema uses a request-time index and a composite request/attempt primary
key, with cascading child deletion. Embedded migrations and their version
record commit under one transaction-scoped advisory lock. Unknown schema
versions fail startup. Use authenticated TLS for remote database connections;
the Compose TLS exception applies only to its private local demo network.

## Read-only operational history API

The control-plane listener is **disabled by default**. Enable it only alongside
PostgreSQL persistence with `ROUTEFORGE_ADMIN_ENABLED=true`; its default address
is `ROUTEFORGE_ADMIN_ADDR=127.0.0.1:8081`. Compose enables it with host publication
restricted to `127.0.0.1:8081`. The inference listener is unchanged.

Admin session authentication is optional outside Compose and enabled in Compose;
see the authentication section below. There is no CORS support. Do not expose
this listener to a remote/shared network. Responses contain operational metadata only: prompts,
responses, credentials, user information, Redis keys, and cached content are
never available. Responses use `Cache-Control: no-store`. This is the future
backend for the local RouteForge Console described below.

- `GET /admin/v1/health`: listener liveness, not a database connectivity probe.
- `GET /admin/v1/requests`: summaries in `started_at DESC, request_id DESC` order.
- `GET /admin/v1/requests/{request_id}`: one summary plus `attempts`, ordered by
  ascending attempt number. Unavailable TTFC, usage and cost remain JSON `null`.
  Cache hits can correctly have `attempt_count: 0` and `attempts: []`.

```sh
curl 'http://127.0.0.1:8081/admin/v1/requests?limit=2&provider=mock'
```

Lists return `{"requests": [...], "next_cursor": "..."}`; `next_cursor` is
`null` on the last page. The default limit is 50, maximum 100. Pass the returned
opaque `cursor` with the same filters for the next page. It encodes only the
last timestamp/ID tuple, never SQL. Pagination uses keysets, not OFFSET; it is
a live view rather than a snapshot across pages, so later inserts/backfills
can change subsequent pages.

Supported filters (combined with AND):

| Filter | Accepted values / semantics |
| --- | --- |
| `provider` | `mock`, `openai`, `anthropic`; matches **initial or final** provider, not every intermediate attempt |
| `routing_policy` | `deterministic`, `latency`, `cost`, `cost_latency` |
| `outcome` | `success`, `timeout`, `unavailable`, `rate_limited`, `invalid_request`, `cancellation`, `internal`, `other_failure` |
| `streaming`, `cache_hit` | `true` or `false` |
| `started_after`, `started_before` | RFC3339 timestamps with at most microsecond precision; exclusive lower/upper bounds |

Unknown/duplicate parameters, malformed IDs/cursors, and invalid limits/filters
return 400. Unknown valid IDs return 404; database failures return a sanitized
503. Only GET is supported. Reads have a two-second deadline and do not affect
routing. Detail reads cap attempts at 100 and fail rather than silently truncate
an oversized chain (current providers cannot approach this bound).

Migration 0003 replaces the timestamp-only index with `(started_at DESC,
request_id DESC)` for stable pagination. No speculative per-filter indexes or
content columns are added. Request/attempt detail reads share a read-only
transaction snapshot. History remains asynchronously recorded and best-effort,
so a just-finished inference request may not appear immediately.

The existing Compose CI also verifies list/detail against PostgreSQL, pagination,
cache-hit zero-attempt history, error statuses, and the metadata-only response
contract through `scripts/verify-history-api.sh`.

## Local RouteForge Console

Start `docker compose up --build -d`, run `./scripts/generate-demo-traffic.sh`,
then open **http://127.0.0.1:3001** and log in with the configured admin secret.
The console inspects real PostgreSQL-backed
request history; Grafana at port 3000 remains the infrastructure/time-series
dashboard. There are no fabricated records. The only write controls are the
authenticated runtime routing settings described below.

The request table exposes metadata, outcomes, duration, provider selection,
attempt/fallback counts, and cache-hit status. Apply the history API's provider,
policy, outcome, streaming, cache-hit, and exclusive timestamp filters; their
state is kept in the URL. Timestamp inputs accept RFC3339 with a timezone, up to
microsecond precision. “Load more” requests one 50-row server-cursor page at a
time. No automatic full-history download or offset pagination occurs. Refresh
starts a new view; there is no background polling.

Open `/requests/{request_id}` to inspect the ordered actual attempt chain.
Unavailable values display as `—`, not zero. Cache hits may have no upstream
attempts; cached fallback responses retain earlier real attempts. TTFC is kept
separate from full duration, and cost is estimated configured USD, not billing.
Integers outside JavaScript's exact display range are labeled explicitly rather
than presented as accurate rounded counts/costs.

`web/` uses pinned React 19.3.0, TypeScript 7.0.2, Vite 8.3.0, a small typed fetch
layer, and npm's checked-in lockfile. The multi-stage container builds with
Node 24.21.0 and serves static assets through non-root Nginx 1.30.4—not Vite's
development server. Same-origin `/api/requests` paths proxy internally to
`routeforge:8081/admin/v1/requests`, including query strings and detail IDs.
The proxy resolves service DNS again after container replacement. Read-only API
routes forward the session cookie but no request bodies. Only authentication
routes and the exact routing-config endpoint forward bounded JSON bodies, Content-Type, and Origin/Fetch Metadata
headers. Authorization headers are never forwarded. Its host port binds loopback
only, with no CORS support. **Do not expose this stack to
remote/shared networks.**

No localStorage, analytics, remote scripts/fonts, response content, or cache
contents are used. The console retains loaded metadata only in page memory;
responses are marked no-store and the proxy has no access log. History is
best-effort and asynchronously written, so a just-finished request can be absent.
Pagination is a live view, not a multi-page snapshot.

For development, enable the local admin listener and use Node 24.21.0:

```sh
cd web
npm ci
npm run dev
# In another terminal, from web/:
npm run typecheck
npm test
npm run build
```

The Vite development server binds loopback and proxies to the local admin API
on port 8081. Fast CI installs from the lockfile, typechecks, tests mocked API
boundaries, and builds. Compose CI verifies built assets, detail deep-links,
read-only proxy behavior, and real persisted cache-hit history. No new workflow
or browser automation service is required.

## Current provider and routing operations

### Runtime routing settings

The Console's **Routing settings** page uses `GET /admin/v1/routing/config`
and `PUT /admin/v1/routing/config` (same-origin `/api/routing/config`). PUT
requires an authenticated admin session, trusted Origin, and JSON; it is forbidden
when admin authentication is disabled. All other control-plane data remains read-only.

A replacement document contains exactly `policy`, `exploration_interval`, and
`max_latency_over_fastest_percent` (integer or explicit `null`). Policies are
`deterministic`, `latency`, `cost`, and `cost_latency`. Exploration accepts whole
numbers 1–1,000,000; tolerance accepts 0–1,000,000 percent and is required for
`cost_latency`. Zero accepts only fastest measured latency ties. Other policies
retain the tolerance as dormant configuration; it is not a universal tradeoff default.

Updates are atomic and apply to new requests. In-flight requests retain one policy
version for routing, fallback, history, metrics and tracing. Exploration counters,
telemetry, circuits, and accounting are preserved; changing the interval applies
the new threshold to retained counters on the next eligible exploration decision.
Explicit-provider mode still uses its configured provider, regardless of policy.
No pricing, model mappings, credentials or provider membership can be changed.

These settings are **process-local and non-durable**: restarting RouteForge restores
environment-configured startup settings. Concurrent saves are last-write-wins.
The UI confirms changes, waits for server acceptance, then rereads settings and
operations. Authentication does not replace TLS or deployment-boundary security;
the console remains local-only. No PostgreSQL configuration persistence is added.

The console's **Overview** (`/overview`) and **Providers** (`/providers`) pages
read `GET /admin/v1/overview` through the same-origin `/api/overview` proxy.
Requests remain at `/`, with their existing filters, pagination and detail pages.
The new endpoint uses the existing default-off, loopback admin listener and
requires no additional configuration. No new database queries or migrations
are involved. Feature flags mean configured enablement, not dependency health.

The response contains `observed_at`, `features`, `routing`, and `providers`:

- Actual active policy and auto/explicit selection mode; configured provider
  order, not a freshly computed routing order.
- Authoritative circuit state, advisory eligibility, probe-in-flight flag,
  OPEN deadline, and already-tracked telemetry success/failure timestamps.
- Per-mode retained/fresh sample counts, sample sufficiency, and fresh rolling
  medians in microseconds. Below the minimum sample count, medians are `null`.
  Completion samples include terminal failures; TTFC requires actual content.
- Pricing coverage counts: model entries with at least one configured rate and
  entries with both rates. Zero-valued configured rates still count. This is
  not a claim that arbitrary native models have pricing; no prices/model tables
  are exposed.
- Sample thresholds/age, exploration interval, and sync/stream warm-up counter
  positions. Counters are `null` when no latency-aware policy is active; dormant
  sample settings are still shown for other policies. Cost-latency tolerance is
  present only when that policy is active.

Inspection only copies state under existing locks. It never calls routing order,
exploration advancement, circuit admission, model resolution, or provider APIs.
An OPEN circuit past its cooldown remains OPEN until an actual attempt claims
the probe; it may already be eligible. HALF_OPEN with an in-flight probe is not
eligible. Latest failure timestamps use provider telemetry's outcome semantics,
not just circuit-counted failures. Components are sampled independently, not in
one global atomic transaction; admission can change immediately afterward.

Refresh is manual. These are process-local snapshots that reset on restart,
not historical charts or health guarantees. Grafana retains its time-series
role. There are no configuration controls, secrets/connection URLs, content,
or CORS additions; admin authentication applies to these views too. The local-only security restrictions above
still apply. Existing Compose smoke verification checks the direct endpoint,
same-origin proxy, and both console routes alongside the history/cache checks.

## Offline benchmark comparisons in the Console

Open **Benchmarks** at `http://127.0.0.1:3001/benchmarks` to compare
`deterministic`, `latency`, `cost`, and `cost_latency` against the built-in
`stable`, `degradation`, `rate_limit`, `streaming`, and `cold_start` fixtures.
These are controlled synthetic experiments, not production traffic, real
provider performance, or semantic-quality measurements. Grafana remains the
historical infrastructure dashboard.

The existing local admin listener provides:

- `GET /admin/v1/benchmarks`: built-in identifiers, version, mode, measured
  request count, and explicit warm-up sequence length.
- `GET /admin/v1/benchmarks/{scenario}?state=warm|cold`: the unchanged benchmark
  comparison JSON for all four policies. State defaults to `warm`.

Only known embedded fixtures and these two initial states are accepted; no
file paths, uploads, URLs, commands, or custom policies can be supplied. The
same-origin console proxy uses `/api/benchmarks`. Existing local-only admin
access restrictions and session authentication apply; there is no CORS addition.

Warm runs replay each fixture's explicit warm-up separately from measured
requests; cold runs start empty. Both reuse `internal/benchmark` with a fresh
virtual-clock gateway per policy. Live routing, circuits, telemetry, cache,
accounting, and PostgreSQL history are not inputs or outputs. Up to ten reports
(five fixtures × two states) are computed lazily once per admin server and
retained as immutable JSON in bounded process memory, not persisted. A fixture
execution failure is sanitized and retained until restart. The CLI is unchanged.

Tables compare success, nearest-rank p50/p95 completion latency or streaming
TTFC, estimated configured cost and cost per successful request, average
attempts/additional fallback attempts, fallback frequency, initial selections,
and switches. Missing values remain unavailable, not zero. Completion includes
failed requests and fallback time; TTFC includes only streams reaching content,
even if they later fail. Cost includes available estimates for all attempts and
can undercount unavailable estimates; it is not invoice cost or money saved.
No weighted score, quality metric, live-provider execution, or editing controls
are introduced. Existing Go/frontend CI and Compose smoke checks cover this
view and verify deterministic results without changing live state.

## Control-plane authentication

The inference API remains unauthenticated. The admin server can require a
single-admin session for **all** history, operations, benchmark, health, and
unknown endpoints. The console checks session status before mounting any
protected view, clears those views on an API 401, and provides Login/Logout.
Static HTML/JS remain public so the Login screen can load; no operational data
is embedded in those assets.

Configuration:

- `ROUTEFORGE_ADMIN_AUTH_ENABLED`: default `false`; Compose sets `true`.
- `ROUTEFORGE_ADMIN_SECRET`: required when enabled, 32–256 bytes without
  surrounding whitespace. Use a randomly generated high-entropy secret, not
  a human password. Keep it in environment/config, never source control or logs.
- `ROUTEFORGE_ADMIN_SESSION_TTL`: default `8h`, whole seconds from `1m` to `24h`.
- `ROUTEFORGE_ADMIN_ORIGIN`: exact browser origin, default
  `http://127.0.0.1:3001`. For Vite use `http://127.0.0.1:5173`. No path, query,
  credentials, or wildcard is accepted. HTTP is allowed only for loopback;
  remote origins require HTTPS.

The same-origin `/api/auth/session` (GET), `/api/auth/login` (POST), and
`/api/auth/logout` (POST) proxy to `/admin/v1/auth/*`. Login accepts only a
small JSON `{ "secret": "..." }` object; logout accepts an empty JSON object.
POSTs require the exact configured Origin, JSON Content-Type, and no cross-site
Fetch Metadata. Missing Origin is rejected, including from command-line clients.
There is no CORS or arbitrary redirect support. Session status exposes only
enabled/authenticated booleans; session identifiers are never returned in JSON.

Credentials are compared via constant-time fixed-length SHA-256 digests. Random
256-bit session IDs are issued in a host-only, Path=/, HttpOnly, SameSite=Strict
cookie; only their hashes and absolute expiry are retained on the server.
Cookies are Secure when the configured browser origin is HTTPS; the explicit
local HTTP demo cannot use Secure cookies. Browser JavaScript never stores
secrets or session IDs in localStorage/sessionStorage. Cookies are host-scoped,
not port-scoped: use a dedicated trusted hostname for any future remote setup.

At most 128 sessions exist per process. Expired entries are pruned during
authentication activity; no worker or database is used. The TTL is absolute,
not sliding. Login rotates an existing session; logout deletes it. Restart or
secret reconfiguration invalidates all sessions. Already-admitted requests can
finish during concurrent logout. A global limit of 20 login attempts per minute
bounds guessing without storing client identities; exhaustion can temporarily
deny login even to the legitimate operator. This is not an internet-facing
abuse-prevention system.

Compose requires the secret explicitly (no hardcoded fallback), keeps all host
ports on loopback, and CI generates a disposable secret without printing it.
The existing smoke checks authenticate using temporary private cookie files,
verify anonymous rejection, Origin enforcement, login, protected views, logout,
and re-login, then remove the cookie files. For manual smoke execution, export
the same secret used by Compose; no provider keys are needed.

TLS, trusted reverse-proxy/network boundaries, and further operational hardening
are still required for remote/shared use. This does not secure the separate
inference, Grafana, or Prometheus interfaces. No users, roles, OAuth, signup,
password reset, or configuration controls are introduced.

## Usage and estimated cost accounting

Every actual provider attempt can contribute provider-reported token usage to
a separate process-local accounting component. OpenAI prompt and completion
tokens and Anthropic input and output tokens are normalized as input, output,
and total usage. Missing usage remains unavailable rather than becoming a
fabricated zero. Fallback attempts are accounted independently using their
resolved provider-native model identifiers; circuit-skipped providers and
model-resolution failures create no accounting attempt.

Streaming usage is consumed internally. OpenAI usage-only stream events and
Anthropic message lifecycle usage do not become assistant content, do not count
as time to first content, and do not commit the downstream SSE response. Usage
reported before a later provider failure or client cancellation is retained,
while the terminal outcome remains a failure or cancellation.

Estimated cost is optional and uses configured input/output USD rates per one
million tokens. The following values are placeholders, not statements of
current provider pricing:

```sh
export ROUTEFORGE_PRICE_OPENAI_INPUT_USD_PER_MILLION="1.234567"
export ROUTEFORGE_PRICE_OPENAI_OUTPUT_USD_PER_MILLION="2.345678"
export ROUTEFORGE_PRICE_ANTHROPIC_INPUT_USD_PER_MILLION="3.456789"
export ROUTEFORGE_PRICE_ANTHROPIC_OUTPUT_USD_PER_MILLION="4.567890"
```

Configuration accepts up to six decimal places. RouteForge converts rates to
integer micro-USD and rounds each attempt's combined input/output estimate
half-up to the nearest micro-USD. Missing usage or a required configured rate
makes the attempt cost unavailable, not zero. Actual usage and accumulated
attempt cost remain observational: the opt-in cost policy compares configured
resolved-model rates before an attempt and does not route from these historical
accounting totals. Pricing never affects deterministic routing, latency
routing, latency exploration, or fallback eligibility.

Accounting retains only bounded provider/model aggregates and resets on
restart. It stores no prompts, response content, credentials, headers, user
identifiers, or raw provider errors, and it has no public endpoint. Estimates
may differ from provider invoices and do not model cached-token discounts,
prompt caching, batch rates, reasoning-token categories, credits, promotions,
or account-specific pricing.

## Offline routing benchmark

Production observations are selection-biased: after RouteForge sends one
request to one provider, that observation does not reveal how every other
provider would have handled the same request. The offline benchmark avoids that
problem by defining a synthetic outcome for every configured provider and
abstract request ID, then replaying the identical scenario through a fresh copy
of the real gateway for each routing policy.

The benchmark uses simulated providers behind the production provider
interfaces. They advance a controlled clock rather than sleeping, so routing,
fallback, telemetry freshness, circuit cooldowns, token accounting, and cost
accounting remain deterministic and fast. Scenarios contain operational
metadata only—no prompts, response text, user identities, credentials, or raw
provider errors—and never contact OpenAI or Anthropic.

Canonical scenarios are versioned under `benchmarks/v1/`. The five embedded
built-ins are `stable`, `degradation`, `rate_limit`, `streaming`, and
`cold_start`, so built-in selection works regardless of the current working
directory. `-state cold` begins measured requests with empty runtime state.
`-state warm` first replays the scenario's explicit warm-up sequence through
the selected policy; those warm-up requests affect telemetry and circuits but
are excluded from measured metrics.

Run all four policies against a warm stable scenario:

```sh
go run ./cmd/routeforge-bench \
  -scenario stable \
  -state warm \
  -policies deterministic,latency,cost,cost_latency
```

An explicit custom fixture uses the same offline-only v1 schema:

```sh
go run ./cmd/routeforge-bench \
  -scenario-file benchmarks/v1/stable.json \
  -policy latency
```

`-scenario` and `-scenario-file` are mutually exclusive, as are `-policy` and
`-policies`. Fixtures are limited to 1 MiB, decoded with unknown fields
rejected, and treated strictly as data. They cannot fetch URLs, run commands,
read other paths, or interpolate environment variables.

A minimal non-streaming fixture looks like:

```json
{
  "version": 1,
  "name": "small-example",
  "mode": "non_streaming",
  "providers": [
    {"name": "provider-a", "model": "model-a"}
  ],
  "inter_request_gap": "100ms",
  "circuit": {"failure_threshold": 2, "open_duration": "1s"},
  "routing": {
    "min_samples": 3,
    "sample_max_age": "30s",
    "exploration_interval": 2,
    "max_latency_over_fastest_percent": 20
  },
  "warmup": [],
  "requests": [
    {"id": "synthetic-1", "providers": {
      "provider-a": {
        "outcome": "success",
        "completion_latency": "220ms",
        "input_tokens": 10,
        "output_tokens": 2
      }
    }}
  ]
}
```

Durations require Go duration units such as `220ms` or `2s`. Supported outcomes
are `success`, `timeout`, `unavailable`, `rate_limited`, `invalid_request`,
`internal`, and `cancellation`. Streaming successes require `ttfc` and
`stream_duration`; failures additionally identify `before_commit` or
`after_commit` with `stream_failure_point`. A pre-commit failure omits `ttfc`.
Only schema version 1 is supported, and the scenario name plus version are
included in benchmark output.

The command writes canonical JSON to stdout. A result is shaped like:

```json
{
  "policy": "latency",
  "mode": "non_streaming",
  "requests": 12,
  "success_rate": 1,
  "p50_latency_ms": 220,
  "p95_latency_ms": 220,
  "estimated_cost_micro_usd": 48300,
  "initial_provider_selections": {"anthropic": 12}
}
```

Reports include successes and failures, fallbacks, average attempts, circuit
skips, post-commit stream failures, provider selections and attempts,
alternate-provider exploration selections, provider switches, token totals,
estimated configured cost, and fallback cost. Synchronous reports calculate
end-to-end completion-latency percentiles. Streaming reports calculate client
TTFC separately from total stream duration. Percentiles use the deterministic
nearest-rank rule: sort observations and select rank `ceil(p*n/100)`.

This enables honest cost-versus-completion-latency and cost-versus-TTFC
comparisons. It does not measure correctness, semantic response quality, human
preference, query difficulty, or task success. RouterBench motivates evaluating
routing tradeoffs empirically, but this harness does not reproduce its semantic
quality benchmark. FrugalGPT motivates heterogeneous cost-conscious serving;
RouteLLM and Hybrid LLM require quality-related signals RouteForge does not
currently possess.

## Model resolution

With an explicit provider, a model name that does not begin with `routeforge/`
is treated as provider-native and passed through unchanged. Provider-native
models are rejected in auto mode because they cannot be safely sent to a
different provider during fallback.

Phase 3A defines one logical model, `routeforge/general`. Configure its
provider-specific targets without embedding commercial model names in source:

```sh
export ROUTEFORGE_MODEL_GENERAL_OPENAI="your-openai-model"
export ROUTEFORGE_MODEL_GENERAL_ANTHROPIC="your-anthropic-model"
```

For every auto attempt, RouteForge starts with the original logical alias and
resolves it for that provider. For example, OpenAI receives the value from
`ROUTEFORGE_MODEL_GENERAL_OPENAI`; after an eligible failure, Anthropic receives
the separate Anthropic mapping. The first provider's model identifier is never
reused for the fallback request. Successful responses expose the original
logical alias to the client.

Auto mode requires a `routeforge/general` mapping for every configured
provider. Explicit provider mode can omit the mapping when it only uses native
model identifiers. The built-in mock maps `routeforge/general` to `mock-model`
for local testing.

To test OpenAI locally, use a real key only in your shell environment:

```sh
export OPENAI_API_KEY="..."
export ROUTEFORGE_PROVIDER="openai"
export ROUTEFORGE_MODEL_GENERAL_OPENAI="your-openai-model"
go run ./cmd/routeforge
```

For Anthropic:

```sh
export ANTHROPIC_API_KEY="..."
export ROUTEFORGE_PROVIDER="anthropic"
export ROUTEFORGE_MODEL_GENERAL_ANTHROPIC="your-anthropic-model"
go run ./cmd/routeforge
```

Do not commit keys or `.env` files. Automated tests use local fake upstream
servers and never make paid API calls.

RouteForge's inference API has no authentication or rate limiting. Admin
authentication does not make the stack safe for public or untrusted networks.
A deployment must explicitly configure
`ROUTEFORGE_ADDR` to listen on a non-loopback interface.

## Test

### Local HTTP performance validation

Phase 9B adds a manual sustained persistence matrix:
`./scripts/run-performance.sh sustained-run-1 sustained`. Cache is disabled;
concurrency 8/32/64 runs for 30 seconds with persistence off/on. Per-request SQL
batching improved measured durable write throughput 1.58–1.74× in the recorded
local comparison, but **85.9–97.0% of history still dropped** under saturation.
Inference stays fail-open; this is not lossless history or a production capacity
claim. See the [sustained results and tradeoffs](benchmarks/performance/README.md#recorded-sustained-results).

`cmd/routeforge-load` measures real RouteForge HTTP overhead against the local
mock provider. It is separate from the deterministic routing-policy simulator:
no real-provider performance or semantic quality is inferred.

```sh
./scripts/run-performance.sh local-run-1
```

Requires Docker Compose, Go and jq. The isolated suite compares concurrency
1/8/32/64, synchronous cache misses/warm hits, and streaming bypass, with
PostgreSQL persistence off/on. Warm-up is excluded. Versioned JSON includes
throughput, success/error rates, nearest-rank p50/p95/p99 client latency and
streaming TTFC, cache evidence, and persistence writes/errors/queue drops.
No provider credentials are needed; no paid calls are made. This is a manual
measurement suite, not a noisy performance gate in PR CI.

See [performance methodology](benchmarks/performance/README.md) for isolation,
bounds, commands, interpretation and limitations. Results are machine-specific;
asynchronous persistence submission latency is not durable commit latency.

The recorded arm64 local baseline completed 24,000 measured mock HTTP requests
without HTTP errors. It also exposed bounded persistence-queue saturation:
6,095 of 12,000 persistence-enabled request histories were dropped under these
bursts. The [raw report](benchmarks/performance/v1/local-arm64-baseline.json)
and methodology distinguish HTTP throughput from durable history coverage;
these are not production capacity claims. No optimization was made.

### Correctness checks

```sh
go test ./...
go vet ./...
go test -race ./...
```
