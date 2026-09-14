export interface RequestSummary {
  request_id: string;
  started_at: string;
  completed_at: string;
  routing_policy: string;
  streaming: boolean;
  logical_model: string;
  initial_provider: string | null;
  final_provider: string | null;
  outcome: string;
  attempt_count: number;
  fallback_count: number;
  request_duration_us: number;
  cache_hit: boolean;
}

export interface Attempt {
  attempt_number: number;
  provider: string;
  resolved_provider_model: string;
  fallback: boolean;
  started_at: string;
  completed_at: string;
  duration_us: number;
  ttfc_us: number | null;
  outcome: string;
  input_tokens: number | null;
  output_tokens: number | null;
  total_tokens: number | null;
  estimated_cost_micro_usd: number | null;
}
export interface RequestDetail extends RequestSummary { attempts: Attempt[] }
export interface RequestPage { requests: RequestSummary[]; next_cursor: string | null }

export const filterOptions = {
  provider: ['mock', 'openai', 'anthropic'],
  routing_policy: ['deterministic', 'latency', 'cost', 'cost_latency'],
  outcome: ['success', 'timeout', 'unavailable', 'rate_limited', 'invalid_request', 'cancellation', 'internal', 'other_failure'],
  streaming: ['true', 'false'],
  cache_hit: ['true', 'false'],
};
export const filterKeys = [...Object.keys(filterOptions), 'started_after', 'started_before'];

// Only the API's supported filters are forwarded; page boundaries come from
// server responses, never from an offset or a client-computed ordering tuple.
export function historyQuery(search: string, cursor?: string): string {
  const source = new URLSearchParams(search);
  const query = new URLSearchParams({ limit: '50' });
  for (const key of filterKeys) {
    const value = source.get(key);
    if (value) query.set(key, value);
  }
  if (cursor) query.set('cursor', cursor);
  return query.toString();
}

export class APIError extends Error {
  constructor(public status: number) { super('History request failed'); }
}

type ObjectValue = Record<string, unknown>;
function object(value: unknown): value is ObjectValue {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}
const text = (value: unknown) => typeof value === 'string' && value.length <= 256;
const integer = (value: unknown) => typeof value === 'number' && Number.isInteger(value) && value >= 0;
function summary(value: unknown): value is RequestSummary {
  return object(value) &&
    ['request_id', 'started_at', 'completed_at', 'routing_policy', 'logical_model', 'outcome'].every(key => text(value[key])) &&
    ['initial_provider', 'final_provider'].every(key => value[key] === null || text(value[key])) &&
    ['streaming', 'cache_hit'].every(key => typeof value[key] === 'boolean') &&
    ['attempt_count', 'fallback_count', 'request_duration_us'].every(key => integer(value[key]));
}
function page(value: unknown): value is RequestPage {
  return object(value) && Array.isArray(value.requests) && value.requests.length <= 100 &&
    value.requests.every(summary) && (value.next_cursor === null || text(value.next_cursor));
}
function detail(value: unknown): value is RequestDetail {
  if (!summary(value) || !('attempts' in value) || !Array.isArray(value.attempts) || value.attempts.length > 100) return false;
  return value.attempts.every((item: unknown) => object(item) &&
    ['provider', 'resolved_provider_model', 'started_at', 'completed_at', 'outcome'].every(key => text(item[key])) &&
    typeof item.fallback === 'boolean' && integer(item.attempt_number) && integer(item.duration_us) &&
    ['ttfc_us', 'input_tokens', 'output_tokens', 'total_tokens', 'estimated_cost_micro_usd'].every(key => item[key] === null || integer(item[key])));
}

async function read<T>(path: string, signal: AbortSignal, valid: (value: unknown) => value is T): Promise<T> {
  const controller = new AbortController();
  const abort = () => controller.abort();
  signal.addEventListener('abort', abort, { once: true });
  if (signal.aborted) abort();
  const timer = setTimeout(abort, 10000);
  try {
    const response = await fetch(path, { signal: controller.signal, cache: 'no-store', credentials: 'omit' });
    if (!response.ok) throw new APIError(response.status);
    const value: unknown = await response.json();
    if (!valid(value)) throw new APIError(0);
    return value;
  } catch (error) {
    if (error instanceof APIError) throw error;
    // Never surface a transport exception or upstream error body to the UI.
    throw new APIError(0);
  } finally {
    clearTimeout(timer);
    signal.removeEventListener('abort', abort);
  }
}

export const historyAPI = {
  list: (search: string, cursor: string | undefined, signal: AbortSignal) =>
    read('/api/requests?' + historyQuery(search, cursor), signal, page),
  detail: (id: string, signal: AbortSignal) =>
    read('/api/requests/' + encodeURIComponent(id), signal, detail),
};

export interface LatencyState {
  stored_samples: number;
  fresh_samples: number;
  sufficient: boolean;
  median_us: number | null;
}
export interface ProviderState {
  provider: string;
  circuit_state: 'closed' | 'open' | 'half_open';
  open_until: string | null;
  eligible: boolean;
  probe_in_flight: boolean;
  last_success: string | null;
  last_failure: string | null;
  completion: LatencyState;
  ttfc: LatencyState;
  priced_models: number;
  complete_price_models: number;
}
export interface Overview {
  observed_at: string;
  features: { cache: boolean; persistence: boolean; metrics: boolean; tracing: boolean };
  routing: {
    policy: string;
    auto: boolean;
    provider_order: string[];
    latency_aware: boolean;
    min_samples: number;
    sample_max_age_us: number;
    exploration_interval: number;
    exploration_counts: [number, number] | null;
    max_latency_over_fastest_percent: number | null;
  };
  providers: ProviderState[];
}
function latencyState(value: unknown): value is LatencyState {
  return object(value) && integer(value.stored_samples) && integer(value.fresh_samples) && typeof value.sufficient === 'boolean' && (value.median_us === null || integer(value.median_us));
}
function overview(value: unknown): value is Overview {
  if (!object(value) || !text(value.observed_at) || !object(value.features) || !object(value.routing) || !Array.isArray(value.providers)) return false;
  const features = value.features, routing = value.routing;
  return ['cache', 'persistence', 'metrics', 'tracing'].every(key => typeof features[key] === 'boolean') &&
    text(routing.policy) && typeof routing.auto === 'boolean' && typeof routing.latency_aware === 'boolean' &&
    Array.isArray(routing.provider_order) && routing.provider_order.every(text) &&
    ['min_samples', 'sample_max_age_us', 'exploration_interval'].every(key => integer(routing[key])) &&
    (routing.exploration_counts === null || Array.isArray(routing.exploration_counts) && routing.exploration_counts.length === 2 && routing.exploration_counts.every(integer)) &&
    (routing.max_latency_over_fastest_percent === null || integer(routing.max_latency_over_fastest_percent)) &&
    value.providers.every((p: unknown) => object(p) && text(p.provider) && ['closed', 'open', 'half_open'].includes(String(p.circuit_state)) &&
      ['open_until', 'last_success', 'last_failure'].every(key => p[key] === null || text(p[key])) &&
      typeof p.eligible === 'boolean' && typeof p.probe_in_flight === 'boolean' && latencyState(p.completion) && latencyState(p.ttfc) &&
      integer(p.priced_models) && integer(p.complete_price_models));
}
export const operationsAPI = {
  overview: (signal: AbortSignal) => read('/api/overview', signal, overview),
};

export type BenchmarkState = 'warm' | 'cold';
export interface BenchmarkScenario {
  id: string; version: number; mode: 'non_streaming' | 'streaming'; requests: number; warmup_requests: number;
}
export interface BenchmarkResult {
  policy: string; mode: 'non_streaming' | 'streaming'; requests: number; success_rate: number;
  average_attempts_per_request: number; fallback_rate: number;
  p50_latency_ms?: number; p95_latency_ms?: number; p50_ttfc_ms?: number; p95_ttfc_ms?: number;
  estimated_cost_micro_usd: number; estimated_cost_per_successful_request_micro_usd?: number;
  initial_provider_selections: Record<string, number>; provider_selection_switches: number;
  fallback_provider_attempts: Record<string, number>;
}
export interface BenchmarkComparison {
  scenario: string; scenario_version: number; state: BenchmarkState; results: BenchmarkResult[];
}
const benchmarkMode = (value: unknown) => value === 'non_streaming' || value === 'streaming';
const nonnegative = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value) && value >= 0;
const distribution = (value: unknown) => object(value) && Object.values(value).every(integer);
function benchmarkCatalog(value: unknown): value is { scenarios: BenchmarkScenario[] } {
  return object(value) && Array.isArray(value.scenarios) && value.scenarios.every((s: unknown) =>
    object(s) && text(s.id) && integer(s.version) && benchmarkMode(s.mode) && integer(s.requests) && integer(s.warmup_requests));
}
function benchmarkComparison(value: unknown): value is BenchmarkComparison {
  return object(value) && text(value.scenario) && integer(value.scenario_version) && (value.state === 'warm' || value.state === 'cold') &&
    Array.isArray(value.results) && value.results.length === 4 && value.results.every((r: unknown) => object(r) &&
      ['deterministic', 'latency', 'cost', 'cost_latency'].includes(String(r.policy)) && benchmarkMode(r.mode) && integer(r.requests) &&
      nonnegative(r.success_rate) && r.success_rate <= 1 && nonnegative(r.fallback_rate) && r.fallback_rate <= 1 && nonnegative(r.average_attempts_per_request) &&
      ['p50_latency_ms', 'p95_latency_ms', 'p50_ttfc_ms', 'p95_ttfc_ms', 'estimated_cost_per_successful_request_micro_usd'].every(key => r[key] === undefined || integer(r[key])) &&
      integer(r.estimated_cost_micro_usd) && integer(r.provider_selection_switches) && distribution(r.initial_provider_selections) && distribution(r.fallback_provider_attempts));
}
export const benchmarkAPI = {
  scenarios: (signal: AbortSignal) => read('/api/benchmarks', signal, benchmarkCatalog),
  compare: (scenario: string, state: BenchmarkState, signal: AbortSignal) =>
    read('/api/benchmarks/' + encodeURIComponent(scenario) + '?state=' + state, signal, benchmarkComparison),
};
