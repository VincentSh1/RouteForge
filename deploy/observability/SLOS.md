# RouteForge example operational SLOs

These are starting points for local evaluation, not universal production
standards. Operators should choose windows and thresholds that match their
traffic, providers, and user expectations.

## Availability

- **Objective:** at least 99% of RouteForge requests have `outcome="success"`
  over a rolling 7-day window.
- **Measurement:**

  ```promql
  sum(increase(routeforge_requests_total{outcome="success"}[7d]))
  /
  clamp_min(sum(increase(routeforge_requests_total[7d])), 1)
  ```

This initial definition includes client errors and cancellations in the
denominator. A deployment may define separate service and client-error SLOs if
its operational contract requires that distinction.

## Non-streaming request latency

- **Objective:** rolling 30-minute p95 RouteForge request duration is below 30
  seconds for `streaming="false"` requests.
- **Measurement:**

  ```promql
  histogram_quantile(
    0.95,
    sum by (le) (
      rate(routeforge_request_duration_seconds_bucket{streaming="false"}[30m])
    )
  )
  ```

## Streaming responsiveness

- **Objective:** rolling 30-minute p95 provider time to first content is below
  3 seconds.
- **Measurement:**

  ```promql
  histogram_quantile(
    0.95,
    sum by (le) (rate(routeforge_provider_ttfc_seconds_bucket[30m]))
  )
  ```

TTFC ends at the first non-empty assistant content. It is not total stream or
request duration. No cost or semantic-quality SLO is defined because RouteForge
has neither an operator budget nor a semantic-quality signal.

The alert thresholds in `prometheus/alerts.yml` are short-window operational
signals, not direct declarations that a long-window SLO has been exhausted.
The checked-in `warning` and `critical` labels communicate relative urgency
only; they do not define paging or escalation policy.
