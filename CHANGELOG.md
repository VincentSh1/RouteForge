# Release notes

## Unreleased — v1.0-style portfolio milestone draft

This summarizes the implemented system, not a fabricated version/tag history.
No v1.0 release has been published by these notes.

- Go OpenAI-compatible synchronous/streaming gateway with mock, OpenAI, and
  Anthropic adapters; typed fallback before stream commitment and circuit breakers.
- Deterministic, latency, cost, and latency-constrained cost routing, with
  provider-specific model resolution and deterministic exploration.
- Versioned deterministic offline policy benchmarks and a separate bounded local
  HTTP performance suite with checked-in burst/sustained measurements.
- Optional OTel tracing, Prometheus metrics, Grafana provisioning, example SLOs,
  and alerts.
- Optional bounded PostgreSQL operational history and fail-open Redis completion
  caching, with explicit content/privacy and unavailable-value semantics.
- Local React Console: history/attempt chains, provider/routing snapshots,
  isolated benchmark comparisons, single-admin sessions, and bounded runtime-only
  routing settings.
- Version-pinned mock-only Compose demo plus Go/frontend and Docker integration CI.
- Measured per-request pgx batching improvement of 1.58–1.74× persistence throughput
  in the documented local sustained comparison. Queue saturation remains; this
  is not lossless history or a production capacity claim.
- Reorganized overview, design decisions, configuration/API/observability references,
  and explicit local-run/security/measurement limitations.
- Evidence-linked portfolio/interview brief, resume bullets, and a manual screenshot
  plan using only real local mock traffic and clearly labeled offline fixtures.

### Try it locally

Follow the [admin-secret setup](README.md#run-the-local-demo), then run:

```sh
docker compose up --build -d --wait
./scripts/generate-demo-traffic.sh
```

Console: http://127.0.0.1:3001 · Grafana: http://127.0.0.1:3000 ·
Prometheus: http://127.0.0.1:9091. See the [demo guide](docs/demo.md) for the
presentation sequence. This draft does not create a Git tag or GitHub Release.

### Evaluation boundaries

Use the [README quickstart](README.md#run-the-local-demo). Default demo traffic
needs no paid provider credentials. Admin authentication does not protect the
separate inference, Grafana, or Prometheus interfaces; remote deployment requires
additional security. Runtime settings/sessions reset on restart, and asynchronous
history can drop records. No semantic-quality benchmark, multi-user system,
distributed routing-state coordination, or production deployment platform is claimed.

See [measured evidence](benchmarks/performance/README.md),
[design tradeoffs](docs/architecture.md), and the existing [MIT license](LICENSE).
