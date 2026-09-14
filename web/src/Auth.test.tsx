import { act, fireEvent, render, screen, within } from '@testing-library/react';
import { expect, it, vi } from 'vitest';
import { Auth, authAPI } from './Auth';
import { App } from './App';
import { historyAPI } from './api';

it('locks content, logs in without storage, and allows authenticated navigation', async () => {
  vi.spyOn(authAPI, 'session').mockResolvedValue({ enabled: true, authenticated: false });
  const login = vi.spyOn(authAPI, 'login').mockResolvedValue({ enabled: true, authenticated: true });
  vi.spyOn(historyAPI, 'list').mockResolvedValue({ requests: [], next_cursor: null });
  const storage = vi.spyOn(Storage.prototype, 'setItem');
  render(<Auth><App /></Auth>);
  expect(screen.getByRole('status')).toHaveTextContent('Checking session');
  expect(screen.queryByRole('navigation')).not.toBeInTheDocument();
  const field = await screen.findByLabelText('Admin secret');
  fireEvent.change(field, { target: { value: 'synthetic-test-input' } });
  fireEvent.click(screen.getByRole('button', { name: 'Login' }));
  await screen.findByRole('navigation');
  expect(login).toHaveBeenCalledWith('synthetic-test-input');
  for (const name of ['Overview', 'Providers', 'Requests', 'Benchmarks']) expect(within(screen.getByRole('navigation')).getByRole('link', { name })).toBeInTheDocument();
  expect(storage).not.toHaveBeenCalled();
  expect(document.cookie).not.toContain('synthetic-test-input');
});

it('sanitizes failed login and clears the input', async () => {
  vi.spyOn(authAPI, 'session').mockResolvedValue({ enabled: true, authenticated: false });
  vi.spyOn(authAPI, 'login').mockRejectedValue(new Error('raw credential sentinel'));
  render(<Auth><p>Protected</p></Auth>);
  const field = await screen.findByLabelText('Admin secret');
  fireEvent.change(field, { target: { value: 'wrong' } });
  fireEvent.click(screen.getByRole('button', { name: 'Login' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Login failed');
  expect(field).toHaveValue('');
  expect(screen.queryByText('Protected')).not.toBeInTheDocument();
  expect(screen.queryByText('raw credential sentinel')).not.toBeInTheDocument();
});

it('returns to Login when a real API boundary receives 401', async () => {
  vi.spyOn(authAPI, 'session').mockResolvedValue({ enabled: true, authenticated: true });
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false, status: 401 }));
  render(<Auth><App /></Auth>);
  expect(await screen.findByRole('heading', { name: 'Login to RouteForge' })).toBeInTheDocument();
  expect(screen.queryByRole('navigation')).not.toBeInTheDocument();
});

it('logs out, hiding protected content, and reports unconfirmed logout honestly', async () => {
  vi.spyOn(authAPI, 'session').mockResolvedValue({ enabled: true, authenticated: true });
  const logout = vi.spyOn(authAPI, 'logout').mockRejectedValueOnce(new Error('private')).mockResolvedValue({ enabled: true, authenticated: false });
  render(<Auth><p>Protected</p></Auth>);
  fireEvent.click(await screen.findByRole('button', { name: 'Logout' }));
  expect(await screen.findByRole('alert')).toHaveTextContent('could not be confirmed');
  fireEvent.click(screen.getByRole('button', { name: 'Logout' }));
  await screen.findByLabelText('Admin secret');
  expect(screen.queryByText('Protected')).not.toBeInTheDocument();
  expect(logout).toHaveBeenCalledTimes(2);
});

it('supports disabled mode and fails closed when session status is unavailable', async () => {
  vi.spyOn(authAPI, 'session').mockRejectedValueOnce(new Error('private')).mockResolvedValue({ enabled: false, authenticated: true });
  render(<Auth><p>Protected</p></Auth>);
  await screen.findByRole('alert');
  expect(screen.queryByText('Protected')).not.toBeInTheDocument();
  fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
  await screen.findByText('Protected');
  expect(screen.queryByRole('button', { name: 'Logout' })).not.toBeInTheDocument();
});

it('uses same-origin cookie requests, never browser persistence', async () => {
  const fetcher = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ authenticated: true }) });
  vi.stubGlobal('fetch', fetcher);
  const storage = vi.spyOn(Storage.prototype, 'setItem');
  await authAPI.login('synthetic-test-input');
  expect(fetcher).toHaveBeenCalledWith('/api/auth/login', expect.objectContaining({ method: 'POST', credentials: 'same-origin', cache: 'no-store', body: JSON.stringify({ secret: 'synthetic-test-input' }) }));
  await authAPI.logout();
  expect(fetcher).toHaveBeenLastCalledWith('/api/auth/logout', expect.objectContaining({ method: 'POST', body: '{}' }));
  expect(storage).not.toHaveBeenCalled();
});

it('does not restore a stale session check after expiration', async () => {
  let resolve!: (value: { enabled: boolean; authenticated: boolean }) => void;
  vi.spyOn(authAPI, 'session').mockReturnValue(new Promise(done => { resolve = done; }));
  render(<Auth><p>Protected</p></Auth>);
  act(() => window.dispatchEvent(new Event('routeforge-session-expired')));
  await act(async () => resolve({ enabled: true, authenticated: true }));
  expect(screen.getByLabelText('Admin secret')).toBeInTheDocument();
  expect(screen.queryByText('Protected')).not.toBeInTheDocument();
});
