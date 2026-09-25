# Configuration and inference reference

[Overview and local demo](../README.md) · [Control plane](control-plane.md) · [Observability](observability.md)

Defaults below describe a standalone process. [Compose](../compose.yml) explicitly enables the local demo dependencies. Never print resolved Compose configuration or environment values when they may contain secrets; use `docker compose config --quiet`.

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
requests keep configured deterministic ordering. Non-streaming and streaming warm-up use
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
are no background probes or distributed circuit coordination.

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
callers; there is no user or tenant isolation.

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
the other local observability volumes. The optional [history API](control-plane.md#read-only-operational-history-api)
exposes this operational metadata on a separate local listener.

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

## Model resolution

With an explicit provider, a model name that does not begin with `routeforge/`
is treated as provider-native and passed through unchanged. Provider-native
models are rejected in auto mode because they cannot be safely sent to a
different provider during fallback.

RouteForge defines one logical model, `routeforge/general`. Configure its
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

Real-provider operation is opt-in and can incur charges. Supply provider credentials
through your environment or secret manager, never in committed files. Use
`ROUTEFORGE_PROVIDER=openai` or `anthropic` with the corresponding key and model
mapping; use `auto` only with the required mappings. The default demo and all
validation use mock/local fake providers.

Do not commit keys or `.env` files. Automated tests use local fake upstream
servers and never make paid API calls.

RouteForge's inference API has no authentication or rate limiting. Admin
authentication does not make the stack safe for public or untrusted networks.
A deployment must explicitly configure
`ROUTEFORGE_ADDR` to listen on a non-loopback interface.
