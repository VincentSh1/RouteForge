import { act, fireEvent, render, screen, within } from '@testing-library/react';
import { expect, it, vi } from 'vitest';
import { App } from './App';
import { APIError, benchmarkAPI } from './api';
import type { BenchmarkComparison, BenchmarkResult, BenchmarkScenario } from './api';

const scenarios: BenchmarkScenario[] = [
  { id: 'stable', version: 1, mode: 'non_streaming', requests: 24, warmup_requests: 8 },
  { id: 'streaming', version: 1, mode: 'streaming', requests: 24, warmup_requests: 8 },
];
const rows: BenchmarkResult[] = ['deterministic', 'latency', 'cost', 'cost_latency'].map(policy => ({
  policy, mode: 'non_streaming', requests: 24, success_rate: 1, average_attempts_per_request: 1.25, fallback_rate: 0.25,
  p50_latency_ms: 120, p95_latency_ms: 220, estimated_cost_micro_usd: 1000000,
  estimated_cost_per_successful_request_micro_usd: 41667, initial_provider_selections: { openai: 20, anthropic: 4 },
  provider_selection_switches: 2, fallback_provider_attempts: { anthropic: 6 },
}));
const result: BenchmarkComparison = { scenario: 'stable', scenario_version: 1, state: 'warm', results: rows };
function setup() {
  window.history.replaceState(null, '', '/benchmarks');
  vi.spyOn(benchmarkAPI, 'scenarios').mockResolvedValue({ scenarios });
  return vi.spyOn(benchmarkAPI, 'compare').mockResolvedValue(result);
}

it('renders a four-policy comparison with explicit synthetic performance semantics', async () => {
  setup(); render(<App />);
  const table = await screen.findByRole('table', { name: 'Policy performance comparison' });
  for (const row of rows) expect(within(table).getByRole('rowheader', { name: row.policy })).toBeInTheDocument();
  expect(table).toHaveTextContent('120 ms / 220 ms');
  expect(table).toHaveTextContent('100.0%');
  expect(table).toHaveTextContent('$1.000000');
  expect(table).toHaveTextContent('$0.041667');
  expect(table).toHaveTextContent('— / —');
  const routing = screen.getByRole('table', { name: 'Policy routing comparison' });
  expect(routing).toHaveTextContent('1.25');
  expect(routing).toHaveTextContent('0.25');
  expect(routing).toHaveTextContent('anthropic: 4 · openai: 20');
  expect(screen.getByText(/not production traffic or real provider performance/)).toBeInTheDocument();
  expect(screen.getByText(/do not measure semantic quality/)).toBeInTheDocument();
  expect(within(screen.getByRole('navigation')).getByRole('link', { name: 'Benchmarks' })).toHaveAttribute('aria-current', 'page');
});

it('changes scenarios and initial state, separating TTFC and unavailable costs', async () => {
  const compare = setup(); render(<App />);
  await screen.findByRole('table', { name: 'Policy performance comparison' });
  const streaming: BenchmarkComparison = { ...result, scenario: 'streaming', results: rows.map(row => ({ ...row, mode: 'streaming', p50_latency_ms: undefined, p95_latency_ms: undefined, p50_ttfc_ms: 30, p95_ttfc_ms: 60, estimated_cost_per_successful_request_micro_usd: undefined })) };
  compare.mockResolvedValue(streaming);
  fireEvent.change(screen.getByLabelText('Scenario'), { target: { value: 'streaming' } });
  expect(await screen.findByRole('heading', { name: 'streaming / warm' })).toBeInTheDocument();
  const table = screen.getByRole('table', { name: 'Policy performance comparison' });
  expect(table).toHaveTextContent('30 ms / 60 ms');
  expect(table).not.toHaveTextContent('120 ms');
  expect(within(table).getAllByText('—').length).toBe(4);
  compare.mockResolvedValue({ ...streaming, state: 'cold' });
  fireEvent.change(screen.getByLabelText('Initial state'), { target: { value: 'cold' } });
  await screen.findByRole('heading', { name: 'streaming / cold' });
  expect(compare).toHaveBeenLastCalledWith('streaming', 'cold', expect.any(AbortSignal));
  expect(screen.getByText(/0 warm-up requests/)).toBeInTheDocument();
});

it('handles loading, sanitized run failure and retry', async () => {
  let reject!: (reason: unknown) => void;
  const compare = setup().mockReturnValueOnce(new Promise((_, fail) => { reject = fail; })).mockResolvedValue(result);
  render(<App />);
  expect(screen.getByRole('status')).toHaveTextContent('Loading benchmark');
  await screen.findByLabelText('Scenario');
  await act(async () => reject(new Error('private transport sentinel')));
  expect(screen.getByRole('alert')).toHaveTextContent('Benchmark unavailable');
  expect(screen.queryByText('private transport sentinel')).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
  await screen.findByRole('table', { name: 'Policy performance comparison' });
  expect(compare).toHaveBeenCalledTimes(2);
});

it('ignores stale responses after switching scenarios', async () => {
  let resolve!: (value: BenchmarkComparison) => void;
  setup().mockReturnValueOnce(new Promise(done => { resolve = done; })).mockResolvedValue({ ...result, scenario: 'streaming' });
  render(<App />);
  fireEvent.change(await screen.findByLabelText('Scenario'), { target: { value: 'streaming' } });
  await screen.findByRole('heading', { name: 'streaming / warm' });
  await act(async () => resolve(result));
  expect(screen.queryByRole('heading', { name: 'stable / warm' })).not.toBeInTheDocument();
});

it('retries catalog failures and handles an empty catalog', async () => {
  setup();
  vi.mocked(benchmarkAPI.scenarios).mockRejectedValueOnce(new Error('private')).mockResolvedValue({ scenarios: [] });
  render(<App />);
  await screen.findByRole('alert');
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
  expect(await screen.findByText('No built-in scenarios available.')).toBeInTheDocument();
});

it('validates fetched reports and constructs only same-origin GET queries', async () => {
  const fetcher = vi.fn().mockResolvedValueOnce({ ok: true, json: async () => ({ scenarios }) })
    .mockResolvedValueOnce({ ok: true, json: async () => result })
    .mockResolvedValueOnce({ ok: true, json: async () => ({ results: [] }) });
  vi.stubGlobal('fetch', fetcher);
  const signal = new AbortController().signal;
  expect(await benchmarkAPI.scenarios(signal)).toEqual({ scenarios });
  expect(await benchmarkAPI.compare('stable', 'cold', signal)).toEqual(result);
  expect(fetcher).toHaveBeenCalledWith('/api/benchmarks/stable?state=cold', expect.objectContaining({ credentials: 'omit', cache: 'no-store' }));
  await expect(benchmarkAPI.compare('stable', 'warm', signal)).rejects.toBeInstanceOf(APIError);
});
