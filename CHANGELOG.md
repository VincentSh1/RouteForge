# Release notes

## Unreleased — portfolio milestone candidate

This summarizes the implemented system, not a fabricated version/tag history.
No v1.0 release has been published by these notes.

- Go OpenAI-compatible synchronous/streaming gateway with mock, OpenAI, and
  Anthropic adapters; typed fallback, circuit breakers, and four routing policies.
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

### Evaluation boundaries

Use the [README quickstart](README.md#run-the-local-demo). Default demo traffic
needs no paid provider credentials. Admin authentication does not protect the
separate inference, Grafana, or Prometheus interfaces; remote deployment requires
additional security. Runtime settings/sessions reset on restart, and asynchronous
history can drop records. No semantic-quality benchmark, multi-user system,
distributed routing-state coordination, or production deployment platform is claimed.

See [measured evidence](benchmarks/performance/README.md),
[design tradeoffs](docs/architecture.md), and the existing [MIT license](LICENSE).
