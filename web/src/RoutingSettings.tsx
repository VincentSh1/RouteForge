import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { APIError, filterOptions, operationsAPI, routingAPI } from './api';
import type { Overview, RoutingSettings as Settings } from './api';

export function RoutingSettings() {
  const [current, setCurrent] = useState<Settings | null>(null);
  const [operations, setOperations] = useState<Overview | null>(null);
  const [policy, setPolicy] = useState('deterministic');
  const [interval, setInterval] = useState('');
  const [tolerance, setTolerance] = useState('');
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState('');
  const [message, setMessage] = useState('');
  const [revision, setRevision] = useState(0);
  const pending = useRef<AbortController | null>(null);
  const changed = current !== null && (policy !== current.policy || interval !== String(current.exploration_interval) || tolerance !== (current.max_latency_over_fastest_percent === null ? '' : String(current.max_latency_over_fastest_percent)));
  function accept(value: Settings) {
    setCurrent(value); setPolicy(value.policy); setInterval(String(value.exploration_interval));
    setTolerance(value.max_latency_over_fastest_percent === null ? '' : String(value.max_latency_over_fastest_percent));
  }
  useEffect(() => {
    const controller = new AbortController(); pending.current = controller;
    setBusy(true); setError('');
    Promise.all([routingAPI.get(controller.signal), operationsAPI.overview(controller.signal)])
      .then(([settings, snapshot]) => { if (!controller.signal.aborted) { accept(settings); setOperations(snapshot); } })
      .catch(() => { if (!controller.signal.aborted) setError('Routing settings are unavailable.'); })
      .finally(() => { if (!controller.signal.aborted) setBusy(false); });
    return () => { controller.abort(); pending.current?.abort(); };
  }, [revision]);
  async function save(event: FormEvent) {
    event.preventDefault(); if (busy) return;
    setMessage(''); setError('');
    const validInteger = (value: string, minimum: number) => /^\d+$/.test(value) && Number(value) >= minimum && Number(value) <= 1000000;
    if (!validInteger(interval, 1) || tolerance !== '' && !validInteger(tolerance, 0) || policy === 'cost_latency' && tolerance === '') {
      setError('Use whole numbers: exploration 1–1,000,000; tolerance 0–1,000,000. Cost latency requires a tolerance.'); return;
    }
    if (!window.confirm('Apply routing settings to new requests? In-flight requests keep their current configuration.')) return;
    const controller = new AbortController(); pending.current = controller; setBusy(true);
    let written = false;
    try {
      await routingAPI.save({ policy, exploration_interval: Number(interval), max_latency_over_fastest_percent: tolerance === '' ? null : Number(tolerance) }, controller.signal);
      written = true;
      const [settings, snapshot] = await Promise.all([routingAPI.get(controller.signal), operationsAPI.overview(controller.signal)]);
      if (!controller.signal.aborted) { accept(settings); setOperations(snapshot); setMessage('Saved. Current server state refreshed.'); }
    } catch (failure) {
      if (!controller.signal.aborted) setError(written ? 'Update accepted, but refresh failed. Retry to inspect current state.' : failure instanceof APIError && failure.status === 403 ? 'An authenticated session and trusted origin are required to save.' : 'Could not save routing settings. Check the values and retry.');
    } finally { if (!controller.signal.aborted) setBusy(false); }
  }
  return <>
    <div className="heading"><div><p className="eyebrow">RUNTIME CONTROL</p><h1>Routing settings</h1><p className="muted">Only routing policy, exploration interval, and latency tolerance can be changed.</p></div></div>
    <div className="notice">Runtime-only · Changes are process-local and return to configured startup defaults after restart. Telemetry, circuits, exploration history, and accounting are preserved.</div>
    {current && <p>Current active policy: <strong>{current.policy}</strong></p>}
    {operations && <p className="hint">Provider order: {operations.routing.provider_order.join(' → ')}. {operations.routing.auto ? 'Automatic provider selection.' : 'Explicit-provider mode: changing policy does not switch the configured provider.'}</p>}
    {error && <div role="alert" className="notice error">{error} <button disabled={busy} onClick={() => setRevision(n => n + 1)}>Retry</button></div>}
    {message && <p role="status" className="notice">{message}</p>}
    {busy && <p role="status">{current ? 'Updating routing settings…' : 'Loading routing settings…'}</p>}
    {current && <form onSubmit={save} className="filters" aria-label="Routing settings">
      <label>Routing policy<select value={policy} disabled={busy} onChange={e => setPolicy(e.target.value)}>{filterOptions.routing_policy.map(p => <option key={p} value={p}>{p}</option>)}</select></label>
      <label>Exploration interval<input inputMode="numeric" value={interval} disabled={busy} onChange={e => setInterval(e.target.value)} /></label>
      <label>Maximum latency over fastest (%)<input inputMode="numeric" value={tolerance} disabled={busy} onChange={e => setTolerance(e.target.value)} placeholder="Not configured" /></label>
      <p className="hint">Exploration applies to latency-aware policies. Tolerance applies only to cost_latency; 0% accepts only fastest-latency ties. Blank leaves tolerance unconfigured.</p>
      <button className="primary" disabled={busy || !changed} type="submit">Save</button>
    </form>}
  </>;
}
