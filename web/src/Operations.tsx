import { useEffect, useState } from 'react';
import { operationsAPI } from './api';
import type { LatencyState, Overview } from './api';
import { duration, timestamp } from './format';

function Telemetry({ value, label }: { value: LatencyState; label: string }) {
  return <div className="telemetry"><dt>{label}</dt><dd>{duration(value.median_us)}</dd>
    <p>{value.fresh_samples} fresh / {value.stored_samples} retained</p>
    <span className={'badge ' + (value.sufficient ? 'success' : 'neutral')}>{value.sufficient ? 'Sufficient samples' : 'Insufficient samples'}</span>
  </div>;
}

export function Operations({ providersOnly = false }: { providersOnly?: boolean }) {
  const [snapshot, setSnapshot] = useState<Overview | null>(null);
  const [error, setError] = useState(false);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setSnapshot(null); setError(false);
    operationsAPI.overview(controller.signal).then(result => { if (!controller.signal.aborted) setSnapshot(result); })
      .catch(() => { if (!controller.signal.aborted) setError(true); });
    return () => controller.abort();
  }, [revision]);
  return <>
    <div className="heading"><div><p className="eyebrow">CURRENT GATEWAY STATE</p><h1>{providersOnly ? 'Providers & routing' : 'Overview'}</h1><p className="muted">Process-local operational state. No historical analytics or configuration controls.</p></div>
      <button disabled={!snapshot && !error} onClick={() => setRevision(n => n + 1)}>Refresh state</button></div>
    {error ? <div className="notice error" role="alert"><strong>Operational state is unavailable</strong><p>Check the local RouteForge connection and try again.</p><button onClick={() => setRevision(n => n + 1)}>Retry</button></div>
      : !snapshot ? <p className="notice" role="status">Loading gateway state…</p> : <>
      <p className="hint">Observed {timestamp(snapshot.observed_at)} · Refresh manually · Independently sampled components, not an atomic gateway-wide snapshot</p>
      {!providersOnly && <><dl className="metadata">{[
        ['Active routing policy', snapshot.routing.policy], ['Selection mode', snapshot.routing.auto ? 'Auto routing' : 'Explicit provider'],
        ['Configured providers', String(snapshot.providers.length)], ['Currently eligible', String(snapshot.providers.filter(p => p.eligible).length)],
        ['Response cache', snapshot.features.cache ? 'Enabled' : 'Disabled'], ['PostgreSQL persistence', snapshot.features.persistence ? 'Enabled' : 'Disabled'],
        ['Metrics', snapshot.features.metrics ? 'Enabled' : 'Disabled'], ['Tracing', snapshot.features.tracing ? 'Enabled' : 'Disabled'],
      ].map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
      <p className="hint">Feature flags describe configuration, not Redis, database, or exporter connectivity.</p>
      <a href="/providers">Inspect provider circuits and routing readiness →</a></>}
      {providersOnly && <>
        <div className="section-heading"><h2>Provider state</h2><span>Eligibility is advisory; admission stays authoritative</span></div>
        {snapshot.providers.length === 0 && <p className="notice">No configured providers.</p>}
        <div className="provider-grid">{snapshot.providers.map(provider => <section className="provider-card" key={provider.provider} aria-label={provider.provider}>
          <div className="attempt-heading"><h3>{provider.provider}</h3><span className={'badge ' + (provider.circuit_state === 'closed' ? 'success' : 'neutral')}>{provider.circuit_state.replaceAll('_', ' ')}</span></div>
          <dl className="metadata compact">
            <div><dt>Routing eligibility</dt><dd>{provider.eligible ? 'Eligible' : 'Not eligible'}</dd></div>
            <div><dt>Probe in flight</dt><dd>{provider.probe_in_flight ? 'Yes' : 'No'}</dd></div>
            <div><dt>Open until</dt><dd>{provider.open_until ? timestamp(provider.open_until) : '—'}</dd></div>
            <div><dt>Pricing availability</dt><dd>{provider.complete_price_models} complete / {provider.priced_models} priced models</dd></div>
            <Telemetry label="Completion median" value={provider.completion} />
            <Telemetry label="Streaming TTFC median" value={provider.ttfc} />
            <div><dt>Latest success</dt><dd>{provider.last_success ? timestamp(provider.last_success) : '—'}</dd></div>
            <div><dt>Latest failure</dt><dd>{provider.last_failure ? timestamp(provider.last_failure) : '—'}</dd></div>
          </dl>
        </section>)}</div>
        <p className="hint">Medians require enough fresh samples; — means unavailable. Non-streaming samples include all terminal attempt outcomes. TTFC requires actual content. Pricing counts model entries with at least one rate; complete means both rates, not universal coverage.</p>
        <p className="hint">An OPEN circuit whose cooldown expired can be eligible without changing state. Inspection never claims a HALF_OPEN probe.</p>
        <div className="section-heading"><h2>Routing configuration & readiness</h2></div>
        <dl className="metadata">{[
          ['Active policy', snapshot.routing.policy], ['Configured order (not a live ranking)', snapshot.routing.provider_order.join(' → ') || '—'],
          ['Latency-aware policy active', snapshot.routing.latency_aware ? 'Yes' : 'No'], ['Minimum fresh samples per mode', String(snapshot.routing.min_samples)],
          ['Sample maximum age', duration(snapshot.routing.sample_max_age_us)], ['Exploration interval', String(snapshot.routing.exploration_interval)],
          ['Warm-up position: sync / stream', snapshot.routing.exploration_counts?.join(' / ') ?? '—'],
          ['Cost-latency tolerance', snapshot.routing.max_latency_over_fastest_percent === null ? '—' : snapshot.routing.max_latency_over_fastest_percent + '%'],
        ].map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
        <p className="hint">Readiness is per mode in the provider cards. Non-latency policies display dormant configured sample thresholds. Exploration positions are bounded warm-up counters, not lifetime totals or a forecast of the next selection. No routing order is computed for this view.</p>
      </>}
    </>}
  </>;
}
