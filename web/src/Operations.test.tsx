import { act, fireEvent, render, screen, within } from '@testing-library/react';
import { expect, it, vi } from 'vitest';
import { App } from './App';
import { APIError, operationsAPI } from './api';
import type { Overview } from './api';

const state: Overview = {
  observed_at: '2026-01-01T00:00:00Z', features: { cache: true, persistence: true, metrics: true, tracing: false },
  routing: { policy: 'latency', auto: true, provider_order: ['openai', 'anthropic'], latency_aware: true, min_samples: 5,
    sample_max_age_us: 300000000, exploration_interval: 10, exploration_counts: [2, 3], max_latency_over_fastest_percent: null },
  providers: [{ provider: 'openai', circuit_state: 'closed', eligible: true, probe_in_flight: false, open_until: null,
    last_success: null, last_failure: null, completion: { stored_samples: 8, fresh_samples: 5, sufficient: true, median_us: 120000 },
    ttfc: { stored_samples: 3, fresh_samples: 0, sufficient: false, median_us: null }, priced_models: 1, complete_price_models: 1 },
    { provider: 'anthropic', circuit_state: 'open', eligible: false, probe_in_flight: false, open_until: '2026-01-01T00:01:00Z',
      last_success: null, last_failure: '2026-01-01T00:00:00Z', completion: { stored_samples: 0, fresh_samples: 0, sufficient: false, median_us: null },
      ttfc: { stored_samples: 0, fresh_samples: 0, sufficient: false, median_us: null }, priced_models: 0, complete_price_models: 0 }],
};

it('shows configured system flags and shared navigation', async () => {
  window.history.replaceState(null, '', '/overview');
  vi.spyOn(operationsAPI, 'overview').mockResolvedValue(state);
  render(<App />);
  expect(await screen.findByText('Auto routing')).toBeInTheDocument();
  expect(screen.getByText('latency')).toBeInTheDocument();
  expect(screen.getByText('Disabled')).toBeInTheDocument();
  const nav = screen.getByRole('navigation');
  expect(within(nav).getByRole('link', { name: 'Overview' })).toHaveAttribute('aria-current', 'page');
  expect(within(nav).getByRole('link', { name: 'Requests' })).toHaveAttribute('href', '/');
  expect(within(nav).getByRole('link', { name: 'Providers' })).toHaveAttribute('href', '/providers');
});

it('shows circuits, readiness, unavailable medians, pricing and routing order', async () => {
  window.history.replaceState(null, '', '/providers');
  vi.spyOn(operationsAPI, 'overview').mockResolvedValue(state);
  render(<App />);
  const openai = await screen.findByRole('region', { name: 'openai' });
  expect(openai).toHaveTextContent('closed');
  expect(openai).toHaveTextContent('120.000 ms');
  expect(openai).toHaveTextContent('5 fresh / 8 retained');
  expect(openai).toHaveTextContent('0 fresh / 3 retained');
  expect(openai).toHaveTextContent('Insufficient samples');
  expect(within(openai).getAllByText('—').length).toBeGreaterThan(0);
  const anthropic = screen.getByRole('region', { name: 'anthropic' });
  expect(anthropic).toHaveTextContent('Not eligible');
  expect(anthropic).toHaveTextContent('0 complete / 0 priced models');
  expect(screen.getByText('openai → anthropic')).toBeInTheDocument();
  expect(screen.getByText('2 / 3')).toBeInTheDocument();
});

it('represents an admitted half-open probe without claiming eligibility', async () => {
  window.history.replaceState(null, '', '/providers');
  vi.spyOn(operationsAPI, 'overview').mockResolvedValue({ ...state, providers: [{ ...state.providers[0], circuit_state: 'half_open', eligible: false, probe_in_flight: true }] });
  render(<App />);
  const provider = await screen.findByRole('region', { name: 'openai' });
  expect(provider).toHaveTextContent('half open');
  expect(provider).toHaveTextContent('Not eligible');
  expect(provider).toHaveTextContent('Probe in flightYes');
});

it('handles loading, sanitized errors, retry and refresh', async () => {
  window.history.replaceState(null, '', '/overview');
  let reject!: (value: unknown) => void;
  const read = vi.spyOn(operationsAPI, 'overview').mockReturnValueOnce(new Promise((_, fail) => { reject = fail; })).mockResolvedValue(state);
  render(<App />);
  expect(screen.getByRole('status')).toHaveTextContent('Loading gateway state');
  await act(async () => reject(new Error('private URL or raw error')));
  expect(screen.getByRole('alert')).toHaveTextContent('Operational state is unavailable');
  expect(screen.queryByText('private URL or raw error')).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
  await screen.findByText('Auto routing');
  fireEvent.click(screen.getByRole('button', { name: 'Refresh state' }));
  await screen.findByText('Auto routing');
  expect(read).toHaveBeenCalledTimes(3);
});

it('validates the real API shape without credentials or state writes', async () => {
  const fetcher = vi.fn().mockResolvedValueOnce({ ok: true, json: async () => state }).mockResolvedValueOnce({ ok: true, json: async () => ({ providers: [] }) });
  vi.stubGlobal('fetch', fetcher);
  const result = await operationsAPI.overview(new AbortController().signal);
  expect(result).toEqual(state);
  expect(fetcher).toHaveBeenCalledWith('/api/overview', expect.objectContaining({ credentials: 'omit', cache: 'no-store' }));
  await expect(operationsAPI.overview(new AbortController().signal)).rejects.toBeInstanceOf(APIError);
});
