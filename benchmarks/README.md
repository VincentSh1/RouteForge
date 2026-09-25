# Deterministic routing benchmarks

[Project overview](../README.md) · [Real HTTP performance measurements](performance/README.md)

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

The command writes canonical JSON to stdout. An illustrative subset of a result
(not a real-provider measurement or the complete output schema) is shaped like:

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

