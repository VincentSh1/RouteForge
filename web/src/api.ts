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
