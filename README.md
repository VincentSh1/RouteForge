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

Authentication, caching, rate limiting, semantic-quality routing,
and an application control-plane UI are intentionally out of scope. Optional
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
RouteForge, PostgreSQL, Prometheus, and Grafana demo. Docker with Compose is
the only runtime prerequisite; the traffic script runs its HTTP client inside
the RouteForge container.

Start the stack from the repository root:

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

Stop the services while retaining local Prometheus and Grafana data:

```sh
docker compose down
```

To deliberately remove the named data volumes as well:

```sh
docker compose down -v
```

The stack uses the mock provider, enables PostgreSQL persistence and metrics,
and leaves OTLP tracing disabled. RouteForge binds `0.0.0.0` only inside its
container so Prometheus can scrape `routeforge:9090` over the private Compose
network. The host API, Prometheus UI, and Grafana ports are restricted to
`127.0.0.1`; neither the RouteForge metrics port nor PostgreSQL is published to
the host.

Mock prices of 2 fictional USD per million input tokens and 8 fictional USD per
million output tokens are configured solely to populate estimated-cost panels.
They are synthetic demo pricing, not commercial pricing or an invoice estimate.
The stack pins Go 1.26.6 (Alpine 3.23 builder), Alpine 3.21.6 runtime,
PostgreSQL 17.11-bookworm, Prometheus 3.13.2, and Grafana 13.2.1
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
It builds the RouteForge image, starts all four services, generates bounded
mock traffic, verifies durable request and attempt rows across a RouteForge
restart, verifies Prometheus scraping, metrics, and alert rules, confirms
Grafana dashboard and datasource provisioning, and always removes the CI
containers and volumes afterward. The workflow uses no provider credentials.

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
the other local observability volumes. Phase 7A provides no request-history
HTTP API.

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

RouteForge has no authentication or rate limiting. Do not expose it
to public or untrusted networks. A deployment must explicitly configure
`ROUTEFORGE_ADDR` to listen on a non-loopback interface.

## Test

```sh
go test ./...
go vet ./...
go test -race ./...
```
