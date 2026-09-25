# Control-plane API and Console

[Local demo and login setup](../README.md#run-the-local-demo) · [Design and security boundaries](architecture.md)

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
password reset, or multi-user system is implemented.

## Read-only operational history API

The control-plane listener is **disabled by default**. Enable it only alongside
PostgreSQL persistence with `ROUTEFORGE_ADMIN_ENABLED=true`; its default address
is `ROUTEFORGE_ADMIN_ADDR=127.0.0.1:8081`. Compose enables it with host publication
restricted to `127.0.0.1:8081`. The inference listener is unchanged.

Admin session authentication is optional outside Compose and enabled in Compose;
see the authentication section below. There is no CORS support. Do not expose
this listener to a remote/shared network. Responses contain operational metadata only: prompts,
responses, credentials, user information, Redis keys, and cached content are
never available. Responses use `Cache-Control: no-store`. This is the backend for the local RouteForge Console.

- `GET /admin/v1/health`: listener liveness, not a database connectivity probe.
- `GET /admin/v1/requests`: summaries in `started_at DESC, request_id DESC` order.
- `GET /admin/v1/requests/{request_id}`: one summary plus `attempts`, ordered by
  ascending attempt number. Unavailable TTFC, usage and cost remain JSON `null`.
  Cache hits can correctly have `attempt_count: 0` and `attempts: []`.

For example, the authenticated Console calls
`/api/requests?limit=2&provider=mock`. A direct unauthenticated `curl` to the
Compose admin listener returns 401; use the Console or an authenticated session,
not a secret in a URL.

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

For development, enable PostgreSQL and the local admin listener, keep authentication
enabled, and set `ROUTEFORGE_ADMIN_ORIGIN=http://127.0.0.1:5173` before starting
RouteForge. Use the Node version pinned in fast CI:

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
The endpoint uses the existing default-off, loopback admin listener and
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
role. These snapshot views are read-only and expose no secrets/connection URLs or content.
The separate routing-settings endpoint is the only configuration write boundary;
admin authentication applies to both. The local-only security restrictions above
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

