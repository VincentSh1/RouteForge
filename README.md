# RouteForge

RouteForge is an OpenAI-compatible AI inference gateway written in Go. It
supports streaming and synchronous chat completions through mock, OpenAI, and Anthropic
providers with explicit selection, fallback, and optional latency- or cost-aware routing.

## API

- `GET /health`
- `POST /v1/chat/completions`
- `system`, `user`, and `assistant` message roles
- Streaming and non-streaming chat completions through provider interfaces
- OpenAI-style success and error responses

Authentication, persistence, caching, rate limiting, balanced latency-and-cost
routing, and observability integrations are intentionally out of scope.

## Requirements

- Go 1.22 or newer

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
configured provider order exactly. `latency` and `cost` are opt-in policies for
auto mode. Explicit provider selection is never redirected by either policy.

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
deterministic; no telemetry endpoint or observability integration is provided.

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
