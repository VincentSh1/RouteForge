import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { App } from './App';
import { APIError, historyAPI, historyQuery } from './api';
import type { Attempt, RequestDetail, RequestSummary } from './api';
import { cost, duration, tokens } from './format';

const id = 'rfreq_AAAAAAAAAAAAAAAAAAAAAQ';
const otherID = 'rfreq_AAAAAAAAAAAAAAAAAAAAAg';
const request: RequestSummary = {
  request_id: id, started_at: '2026-01-01T00:00:00Z', completed_at: '2026-01-01T00:00:01Z',
  routing_policy: 'cost_latency', streaming: false, logical_model: 'routeforge/general',
  initial_provider: 'openai', final_provider: 'anthropic', outcome: 'success', attempt_count: 2,
  fallback_count: 1, request_duration_us: 1000000, cache_hit: false,
};
const attempt: Attempt = {
  attempt_number: 1, provider: 'openai', resolved_provider_model: 'configured-native-model', fallback: false,
  started_at: request.started_at, completed_at: request.completed_at, duration_us: 1000000,
  ttfc_us: null, outcome: 'timeout', input_tokens: null, output_tokens: null, total_tokens: null, estimated_cost_micro_usd: null,
};
function showDetail(value: RequestDetail) {
  window.history.replaceState(null, '', '/requests/' + id);
  vi.spyOn(historyAPI, 'detail').mockResolvedValue(value);
  return render(<App />);
}

describe('history console', () => {
  it('renders request metadata and a detail link without response content', async () => {
    vi.spyOn(historyAPI, 'list').mockResolvedValue({ requests: [request], next_cursor: null });
    render(<App />);
    expect(await screen.findByRole('link', { name: id })).toHaveAttribute('href', '/requests/' + id);
    const table = screen.getByRole('table');
    for (const text of ['cost_latency', 'Non-streaming', 'routeforge/general', 'openai → anthropic', 'success', '2 / 1', '1.000 s', 'No hit']) {
      expect(within(table).getByText(text)).toBeInTheDocument();
    }
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();
  });

  it('stores filters in the URL and resets the cursor on a filter change', async () => {
    const list = vi.spyOn(historyAPI, 'list').mockResolvedValue({ requests: [request], next_cursor: 'opaque-one' });
    render(<App />);
    await screen.findByText(id);
    fireEvent.change(screen.getByLabelText('Provider'), { target: { value: 'mock' } });
    fireEvent.change(screen.getByLabelText('Streaming'), { target: { value: 'false' } });
    fireEvent.change(screen.getByLabelText('Started after'), { target: { value: '2026-01-01T00:00:00Z' } });
    fireEvent.submit(screen.getByRole('form', { name: 'History filters' }));
    await waitFor(() => expect(list).toHaveBeenLastCalledWith(window.location.search, undefined, expect.any(AbortSignal)));
    expect(new URLSearchParams(window.location.search).get('provider')).toBe('mock');
    expect(new URLSearchParams(window.location.search).get('streaming')).toBe('false');
    fireEvent.click(screen.getByRole('button', { name: 'Reset' }));
    await waitFor(() => expect(window.location.search).toBe(''));
  });

  it('loads only the requested cursor page and retains earlier rows on retry', async () => {
    const list = vi.spyOn(historyAPI, 'list')
      .mockResolvedValueOnce({ requests: [request], next_cursor: 'opaque+/=' })
      .mockRejectedValueOnce(new APIError(503))
      .mockResolvedValueOnce({ requests: [{ ...request, request_id: otherID }], next_cursor: null });
    render(<App />);
    await screen.findByText(id);
    expect(list).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole('button', { name: 'Load more' }));
    await screen.findByRole('alert');
    expect(screen.getByText(id)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    await screen.findByText(otherID);
    expect(list).toHaveBeenLastCalledWith('', 'opaque+/=', expect.any(AbortSignal));
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();
  });

  it('handles loading, empty history, and a safe retryable error', async () => {
    let reject!: (error: unknown) => void;
    vi.spyOn(historyAPI, 'list').mockReturnValueOnce(new Promise((_, fail) => { reject = fail; }))
      .mockResolvedValueOnce({ requests: [], next_cursor: null });
    render(<App />);
    expect(screen.getByRole('status')).toHaveTextContent('Loading history');
    await act(async () => reject(new Error('secret upstream detail')));
    expect(screen.getByRole('alert')).toHaveTextContent('History is unavailable');
    expect(screen.queryByText('secret upstream detail')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    await screen.findByText('No requests found');
  });

  it('renders ordered attempts, nulls, zero tokens, and fixed-point cost', async () => {
    showDetail({ ...request, attempts: [
      { ...attempt, attempt_number: 2, provider: 'anthropic', fallback: true, outcome: 'success', input_tokens: 0, output_tokens: 2, total_tokens: 2, ttfc_us: 500, estimated_cost_micro_usd: 16 },
      attempt,
    ] });
    const chain = await screen.findByRole('list');
    const rows = within(chain).getAllByRole('listitem');
    expect(rows[0]).toHaveTextContent('#1 · openai');
    expect(rows[1]).toHaveTextContent('#2 · anthropic');
    expect(rows[1]).toHaveTextContent('Fallback attempt');
    expect(within(rows[0]).getAllByText('—')).toHaveLength(5);
    expect(rows[1]).toHaveTextContent('$0.000016');
    expect(within(rows[1]).getByText('0')).toBeInTheDocument();
    expect(rows[1]).toHaveTextContent('0.500 ms');
  });

  it('shows cache hits without fabricating attempts', async () => {
    showDetail({ ...request, cache_hit: true, attempt_count: 0, fallback_count: 0, attempts: [] });
    await screen.findByText('Served from response cache');
    expect(screen.getByText('No upstream provider attempts were recorded.')).toBeInTheDocument();
    expect(screen.queryByRole('listitem')).not.toBeInTheDocument();
  });

  it('distinguishes request 404 and detail loading', async () => {
    window.history.replaceState(null, '', '/requests/' + id);
    vi.spyOn(historyAPI, 'detail').mockRejectedValue(new APIError(404));
    render(<App />);
    expect(screen.getByRole('status')).toHaveTextContent('Loading request');
    await screen.findByText('Request not found');
    expect(screen.queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument();
  });

  it('ignores a stale result after a filter change', async () => {
    let resolve!: (page: { requests: RequestSummary[]; next_cursor: null }) => void;
    vi.spyOn(historyAPI, 'list').mockReturnValueOnce(new Promise(done => { resolve = done; }))
      .mockResolvedValueOnce({ requests: [], next_cursor: null });
    render(<App />);
    fireEvent.change(screen.getByLabelText('Cache hit'), { target: { value: 'true' } });
    fireEvent.submit(screen.getByRole('form', { name: 'History filters' }));
    await screen.findByText('No requests found');
    await act(async () => resolve({ requests: [request], next_cursor: null }));
    expect(screen.queryByText(id)).not.toBeInTheDocument();
  });
});

describe('typed API boundary', () => {
  it('forwards supported filters and safely encodes the server cursor', () => {
    const query = new URLSearchParams(historyQuery('?provider=mock&routing_policy=cost&outcome=success&streaming=false&cache_hit=true&started_after=2026-01-01T00:00:00Z&started_before=2026-02-01T00:00:00Z&offset=1000&cursor=untrusted', 'opaque+/='));
    expect(query.get('limit')).toBe('50');
    expect(query.get('cursor')).toBe('opaque+/=');
    expect(query.get('offset')).toBeNull();
    expect(query.size).toBe(9);
  });
  it('fetches same-origin metadata without credentials or browser caching', async () => {
    const fetcher = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ requests: [], next_cursor: null }) });
    vi.stubGlobal('fetch', fetcher);
    await historyAPI.list('?provider=mock', undefined, new AbortController().signal);
    expect(fetcher).toHaveBeenCalledWith('/api/requests?limit=50&provider=mock', expect.objectContaining({ credentials: 'same-origin', cache: 'no-store' }));
  });
  it('does not read or expose raw error bodies', async () => {
    const json = vi.fn();
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 503, json }));
    await expect(historyAPI.detail(id, new AbortController().signal)).rejects.toMatchObject({ status: 503, message: 'History request failed' });
    expect(json).not.toHaveBeenCalled();
  });
  it('treats a malformed success body as an API failure', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ requests: [null] }) }));
    await expect(historyAPI.list('', undefined, new AbortController().signal)).rejects.toBeInstanceOf(APIError);
  });
  it('preserves unavailable and zero values without silently rounding large integers', () => {
    expect(tokens(null)).toBe('—');
    expect(tokens(0)).toBe('0');
    expect(duration(0)).toBe('0.000 ms');
    expect(cost(0)).toBe('$0.000000');
    expect(cost(Number.MAX_SAFE_INTEGER + 1)).toBe('Outside display range');
  });
});
