# Observability reference

[Overview](../README.md) · [Architecture](architecture.md) · [Example SLOs](../deploy/observability/SLOS.md)

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
not inject RouteForge trace headers into OpenAI or Anthropic requests.
Traces contain operational categories and bounded identifiers only.
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
defaults to loopback. This listener has no authentication. Operators who
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
| `routeforge_persistence_submitted_total` | counter | Submissions while accepting, including full-queue rejections |
| `routeforge_persistence_queue_depth` | gauge | Buffered records, excluding the active write |
| `routeforge_persistence_writes_total` | counter | Completed store calls, successful or failed |
| `routeforge_persistence_write_duration_seconds_total` | counter | Cumulative store-call seconds; excludes queue wait |
| `routeforge_cache_lookups_total` | counter | Bounded hit/miss/error outcomes |
| `routeforge_cache_writes_total` | counter | Bounded success/error outcomes |

For example, a scrape may contain:

```text
routeforge_requests_total{outcome="success",routing_policy="deterministic",streaming="false"} 1
routeforge_provider_attempts_total{fallback="false",outcome="success",provider="mock",streaming="false"} 1
routeforge_tokens_total{direction="input",provider="mock"} 3
```

Metrics use only bounded labels: configured provider name, routing policy,
streaming/fallback booleans, typed outcome, circuit state, token direction, and
bounded cache/persistence results. Persistence queue/write instruments are label-free.
Arbitrary model names, prompts, responses, bodies, credentials, authorization
headers, user/request/trace identifiers, URLs, raw errors, and filesystem paths
are not labels. Missing usage or pricing produces no fabricated token or
zero-cost observation. The exporter uses a private Prometheus registry, so the
endpoint does not automatically publish Go runtime or process collectors.

Tracing and metrics support all four combinations independently: both off,
tracing only, metrics only, or both on. Metrics disabled starts no Prometheus
exporter or listener; tracing disabled still requires no OTLP endpoint.

## Observability dashboard

RouteForge provides a curated, provisionable local monitoring path without
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
loads [the RouteForge alert rules](../deploy/observability/prometheus/alerts.yml).
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
[RouteForge Overview dashboard](../deploy/observability/grafana/dashboards/routeforge-overview.json)
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

[Example operational SLOs](../deploy/observability/SLOS.md) define adjustable
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
