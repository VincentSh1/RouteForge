# Local demo and screenshot guide

[Setup and URLs](../README.md#run-the-local-demo) ·
[Interview brief](portfolio.md) · [Troubleshooting](development.md#local-demo-troubleshooting)

This is a capture plan, not a gallery of completed screenshots. Use the running
mock-only stack and the browser's normal screenshot tools; no browser automation,
fabricated metrics, edited UI values, or remote providers are needed.

## Prepare a truthful local session

1. Follow the README secret setup and Compose start instructions. Keep the default
   `mock` provider and synthetic prices. Do not introduce OpenAI/Anthropic keys.
   Use a local demo database containing only synthetic traffic, not imported history.
2. Generate the documented 40 requests, then log in at
   `http://127.0.0.1:3001`. Never capture login entry, password-manager overlays,
   cookies, developer tools, terminal environment output, or connection URLs.
3. Confirm Overview reports explicit-provider mode with **mock**. A single provider
   is the truthful default; don't pretend this is live multi-provider traffic.
4. For a more readable Grafana time window, optionally send 120 additional requests
   in six batches with 10-second pauses, from the repository root:

   ```sh
   for batch in 1 2 3 4 5 6; do
     ./scripts/generate-demo-traffic.sh 20 || break
     sleep 10
   done
   ```

   This is paced demonstration traffic, not a load test or readiness check. Stop
   if a batch fails. Keep the stock script unchanged: sync requests reuse an eligible
   cache key; streaming bypasses cache. The loop never runs indefinitely.
5. In Grafana select the provisioned **RouteForge Overview** dashboard and a recent
   window such as Last 5 minutes. Allow at least two successful 15-second scrapes
   before interpreting rates; refresh if needed. Include the time picker in captures.

The Console requires manual refresh. History is asynchronous; wait for rows rather
than inventing them. Do not flush Redis, edit PostgreSQL rows, force circuit failures,
or change routing settings just to make a screenshot look busier.

## Capture plan

Use a consistent desktop viewport (for example 1440 × 1000 at 100% zoom), readable
text, and app-only crops. Keep metric units, explanatory notices, and filter/scenario
context visible. Do not crop away caveats that change what a number means.

Suggested filenames below are destinations for **future real captures**, not
existing assets. If publishing them later, use `docs/assets/` and add image links
only after capture and privacy review. Start with request detail, benchmark comparison,
and Grafana; the remaining views support a longer walkthrough.

| Suggested file | Exact view and action | What to keep visible / caption |
| --- | --- | --- |
| `console-overview.png` | Console `/overview`; click **Refresh state** after traffic | Active policy, explicit selection mode, mock provider count, configured feature flags. Caption: “Local mock gateway snapshot; enabled flags are configuration, not dependency health.” |
| `console-providers.png` | Console `/providers`; capture the mock card and **Routing configuration & readiness** (two crops if needed) | Circuit state, advisory eligibility, fresh/retained samples, medians, pricing coverage, configured order. Caption: “Current process-local mock state; inspection does not claim a circuit probe.” |
| `console-history.png` | Console `/`; reset filters and refresh | Several real rows, sync/stream mode, outcome, attempts, and cache status. Caption: “Synthetic local requests recorded as operational metadata, not content.” |
| `console-request-detail.png` | On `/`, set **Streaming = true**, apply, then open an actual row | Ordered mock attempt, duration, TTFC, token usage, estimated configured cost. Caption: “One real local streaming attempt; TTFC is distinct from stream lifetime.” |
| `console-cache-hit.png` | On `/`, reset filters, set **Cache hit = true**, apply, open an actual row | Cache notice and zero actual attempts when there were no prior failed invocations. Caption: “Cached completion with no new provider invocation or provider usage charge.” |
| `console-benchmark.png` | Console `/benchmarks`; select **degradation**, **Warm** | All four policy rows, cost/latency/success units, scenario/state and synthetic-data notice. Caption: “Deterministic counterfactual fixture replay, not live provider performance.” |
| `console-benchmark-streaming.png` | Same page, select **streaming**, **Warm** | TTFC comparison and unavailable completion fields as displayed. Caption: “Synthetic streaming responsiveness comparison; no semantic-quality score.” |
| `grafana-overview.png` | Grafana `http://127.0.0.1:3000/d/routeforge-overview`; Last 5 minutes, All filters | Overview stats and request/provider duration sections; use a second crop for TTFC/tokens if needed. Caption: “Real local mock traffic; costs use fictional configured prices, not provider billing.” |

Optional: capture Console `/routing` to explain the existing authenticated controls.
Keep the **Runtime-only** notice and explicit-provider warning visible; do not click
Save during this presentation-only walkthrough. A configured policy in mock mode
does not demonstrate switching between commercial providers.

## Expected gaps are part of the demonstration

- Cached synchronous requests do not add completion-latency samples. The mock may
  therefore show an unavailable completion median while streaming has enough TTFC
  samples. Preserve `—` and “Insufficient samples”; they are not broken UI.
- With deterministic routing, exploration positions/tolerance can be unavailable
  or dormant. Do not imply the displayed order is a newly computed live ranking.
- Successful mock traffic does not exercise live fallback or OPEN/HALF_OPEN states.
  Those panels may be empty. Use built-in benchmark scenarios to discuss failures,
  explicitly identifying them as simulations—not as live incidents or history rows.
- Grafana rates and range increases need scrape history and may be fractional.
  They are not exact lifetime counts. No-traffic ratios can be unavailable.
- A zero-attempt cache hit is expected, not missing instrumentation. A just-completed
  request may also be absent from PostgreSQL because history is best-effort.

## A short live walkthrough

1. **Overview → Providers:** explain configuration versus health, process-local
   state, and why reading a snapshot cannot mutate admission or exploration.
2. **Requests → streaming detail → cache-hit detail:** contrast a real invocation
   with cache reuse; point out NULL/unavailable semantics and metadata-only history.
3. **Benchmarks:** compare all four policies under identical fixture conditions;
   switch to streaming and separate TTFC from full duration and semantic quality.
4. **Grafana:** show aggregate observations from the mock workload. Explain why this
   complements the Console rather than duplicating request history.
5. **Measured results:** use the checked-in sustained report for the persistence
   story, not the demo chart. State both the 1.58–1.74× gain and remaining queue loss.

## Before publishing any asset

- Use a clean browser window without personal tabs, bookmarks, account avatars,
  notifications, filesystem paths, secrets, or credentials. Capture only synthetic
  request identifiers already displayed by the metadata UI—not user data.
- Review the image and image metadata. Prefer recapturing a clean frame over
  redaction; never edit data values, combine runs to imply one run, or use generated
  imagery as a dashboard screenshot.
- Caption each image “local mock workload” or “offline synthetic benchmark” as
  appropriate. Record source commit, capture date, workload count/command, filters,
  time window, and scenario/state beside the eventual gallery. Do not record machine
  usernames, paths, secrets, or raw payloads.
- Check the screenshot can be read at README size. Publish only a few useful images;
  no placeholder/broken image links are needed while capture is pending.

Log out afterward. `docker compose down` stops the demo while retaining local
data; only use `docker compose down -v` if you intend to delete those volumes.
