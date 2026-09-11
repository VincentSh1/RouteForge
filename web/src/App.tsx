import { useEffect, useRef, useState, useSyncExternalStore } from 'react';
import type { FormEvent } from 'react';
import { APIError, filterKeys, filterOptions, historyAPI } from './api';
import type { RequestSummary, RequestDetail } from './api';
import { cost, duration, timestamp, tokens } from './format';
import { Operations } from './Operations';

function subscribe(callback: () => void) {
  window.addEventListener('popstate', callback);
  return () => window.removeEventListener('popstate', callback);
}
function navigate(url: string) {
  window.history.pushState(null, '', url);
  window.dispatchEvent(new PopStateEvent('popstate'));
}
function Badge({ value }: { value: string }) {
  return <span className={'badge ' + (value === 'success' ? 'success' : 'neutral')}>{value.replaceAll('_', ' ')}</span>;
}
function Failure({ error, retry }: { error: unknown; retry: () => void }) {
  const status = error instanceof APIError ? error.status : 0;
  return <div className="notice error" role="alert">
    <strong>{status === 404 ? 'Request not found' : status === 400 ? 'Invalid history query' : 'History is unavailable'}</strong>
    <p>{status === 404 ? 'This request is not present in durable history.' : status === 400 ? 'Check the filters and timestamp format, or reset the filters.' : 'Check that RouteForge and PostgreSQL are available, then try again.'}</p>
    {status !== 404 && <button onClick={retry}>Retry</button>}
  </div>;
}

const labels: Record<string, string> = { provider: 'Provider', routing_policy: 'Routing policy', outcome: 'Outcome', streaming: 'Streaming', cache_hit: 'Cache hit' };
function Filters({ search }: { search: string }) {
  const values = new URLSearchParams(search);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const query = new URLSearchParams();
    for (const key of filterKeys) {
      const value = String(data.get(key) ?? '').trim();
      if (value) query.set(key, value);
    }
    navigate('/' + (query.size ? '?' + query.toString() : ''));
  }
  return <form key={search} onSubmit={submit} className="filters" aria-label="History filters">
    {Object.entries(filterOptions).map(([key, options]) => <label key={key}>{labels[key]}
      <select name={key} defaultValue={values.get(key) ?? ''}><option value="">All</option>{options.map(value => <option key={value} value={value}>{value.replaceAll('_', ' ')}</option>)}</select>
    </label>)}
    <label>Started after<input name="started_after" defaultValue={values.get('started_after') ?? ''} placeholder="2026-01-01T00:00:00Z" maxLength={40} /></label>
    <label>Started before<input name="started_before" defaultValue={values.get('started_before') ?? ''} placeholder="RFC3339 timestamp" maxLength={40} /></label>
    <div className="filter-actions"><button className="primary" type="submit">Apply filters</button><button type="button" onClick={() => navigate('/')}>Reset</button></div>
    <p className="hint">Provider matches the initial or final provider. Time bounds are exclusive; use RFC3339 with a timezone, up to microsecond precision.</p>
  </form>;
}

function History({ search }: { search: string }) {
  const [rows, setRows] = useState<RequestSummary[]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const [revision, setRevision] = useState(0);
  const controller = useRef<AbortController | null>(null);
  const busy = useRef(false);
  async function load(cursor?: string) {
    if (busy.current) return;
    busy.current = true;
    const current = new AbortController();
    controller.current = current;
    setLoading(true); setError(null);
    try {
      const page = await historyAPI.list(search, cursor, current.signal);
      if (current.signal.aborted) return;
      setRows(old => cursor ? [...old, ...page.requests.filter(row => !old.some(item => item.request_id === row.request_id))] : page.requests);
      setNext(page.next_cursor);
    } catch (failure) {
      if (!current.signal.aborted) setError(failure);
    } finally {
      if (!current.signal.aborted) { setLoading(false); busy.current = false; }
    }
  }
  useEffect(() => {
    busy.current = false;
    void load();
    return () => { controller.current?.abort(); };
    // Each URL query mounts a fresh History; revision deliberately refreshes it.
  }, [revision]);
  return <>
    <div className="heading"><div><p className="eyebrow">OPERATIONAL HISTORY</p><h1>Requests</h1><p className="muted">Follow each request from routing decision to terminal outcome.</p></div><button disabled={loading} onClick={() => { setRows([]); setNext(null); setRevision(n => n + 1); }}>Refresh</button></div>
    <Filters search={search} />
    <div className="section-heading"><h2>Request history</h2><span>{rows.length} loaded · newest first</span></div>
    {error != null && <Failure error={error} retry={() => void load(rows.length ? next ?? undefined : undefined)} />}
    {!loading && !error && rows.length === 0 && <div className="notice"><strong>No requests found</strong><p>Adjust the filters or generate local mock traffic. Completed requests may take a moment to appear.</p></div>}
    {rows.length > 0 && <div className="table-wrap"><table><caption className="sr-only">Operational request history</caption><thead><tr>{['Request / started at', 'Policy / mode', 'Model', 'Initial → final', 'Outcome', 'Attempts / fallbacks', 'Duration', 'Cache'].map(name => <th key={name}>{name}</th>)}</tr></thead>
      <tbody>{rows.map(row => <tr key={row.request_id}>
        <td><a className="mono" href={'/requests/' + encodeURIComponent(row.request_id) + search}>{row.request_id}</a><small>{timestamp(row.started_at)}</small></td>
        <td>{row.routing_policy}<small>{row.streaming ? 'Streaming' : 'Non-streaming'}</small></td>
        <td className="model">{row.logical_model}</td><td>{row.initial_provider ?? '—'} → {row.final_provider ?? '—'}</td>
        <td><Badge value={row.outcome} /></td><td className="numeric">{row.attempt_count} / {row.fallback_count}</td><td className="numeric">{duration(row.request_duration_us)}</td><td>{row.cache_hit ? 'Hit' : 'No hit'}</td>
      </tr>)}</tbody></table></div>}
    {loading && <p className="notice" role="status">Loading history…</p>}
    {next && <div className="pagination"><button disabled={loading} onClick={() => void load(next)}>Load more</button><span className="muted">Up to 50 requests per page · server cursor</span></div>}
  </>;
}

function Detail({ id, search }: { id: string; search: string }) {
  const [record, setRecord] = useState<RequestDetail | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setRecord(null); setError(null);
    historyAPI.detail(id, controller.signal).then(value => { if (!controller.signal.aborted) setRecord(value); })
      .catch(failure => { if (!controller.signal.aborted) setError(failure); });
    return () => controller.abort();
  }, [id, revision]);
  return <>
    <a className="back" href={'/' + search}>← Request history</a>
    <div className="heading"><div><p className="eyebrow">REQUEST INSPECTION</p><h1>Request detail</h1><p className="mono request-id">{id}</p></div>{record && <Badge value={record.outcome} />}</div>
    {error != null ? <Failure error={error} retry={() => setRevision(n => n + 1)} /> : !record ? <p role="status" className="notice">Loading request…</p> : <>
      <dl className="metadata">{[
        ['Started', timestamp(record.started_at)], ['Completed', timestamp(record.completed_at)],
        ['Routing policy', record.routing_policy], ['Mode', record.streaming ? 'Streaming' : 'Non-streaming'],
        ['Logical model', record.logical_model], ['Initial provider', record.initial_provider ?? '—'],
        ['Final provider', record.final_provider ?? '—'], ['Request duration', duration(record.request_duration_us)],
        ['Actual attempts', record.attempt_count], ['Fallbacks', record.fallback_count], ['Cache hit', record.cache_hit ? 'Yes' : 'No'],
      ].map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
      <div className="section-heading"><h2>Provider attempt chain</h2><span>Actual upstream invocations only</span></div>
      {record.cache_hit && <div className="notice cache"><strong>Served from response cache</strong><p>No new upstream call was made for the cached completion. Any earlier real attempts remain listed below.</p></div>}
      {record.attempts.length === 0 && <div className="notice">No upstream provider attempts were recorded.</div>}
      <ol className="attempts">{[...record.attempts].sort((a, b) => a.attempt_number - b.attempt_number).map(attempt => <li key={attempt.attempt_number}>
        <div className="attempt-heading"><h3>#{attempt.attempt_number} · {attempt.provider}</h3><Badge value={attempt.outcome} /><span>{attempt.fallback ? 'Fallback attempt' : 'Initial attempt'}</span></div>
        <p className="mono">{attempt.resolved_provider_model}</p>
        <dl className="metadata compact">{[
          ['Started', timestamp(attempt.started_at)], ['Completed', timestamp(attempt.completed_at)],
          ['Duration', duration(attempt.duration_us)], ['TTFC', duration(attempt.ttfc_us)],
          ['Input tokens', tokens(attempt.input_tokens)], ['Output tokens', tokens(attempt.output_tokens)],
          ['Total tokens', tokens(attempt.total_tokens)], ['Estimated configured cost', cost(attempt.estimated_cost_micro_usd)],
        ].map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
      </li>)}</ol>
      <p className="hint">— means unavailable, not zero. TTFC measures first assistant content, not total stream lifetime. Cost is configured micro-USD displayed as USD, not invoice cost.</p>
    </>}
  </>;
}

export function App() {
  const location = useSyncExternalStore(subscribe, () => window.location.pathname + window.location.search);
  const url = new URL(location, window.location.origin);
  const match = /^\/requests\/(rfreq_[A-Za-z0-9_-]{22})$/.exec(url.pathname);
  return <div className="app"><header><a className="brand" href="/"><span className="brand-mark">RF</span>RouteForge <span className="muted">/ Console</span></a><span className="local"><span />LOCAL · READ ONLY</span></header>
    <nav aria-label="Console navigation">{[['/overview', 'Overview'], ['/providers', 'Providers'], ['/', 'Requests']].map(([href, label]) => <a key={href} href={href} aria-current={(url.pathname === href || href === '/' && match) ? 'page' : undefined}>{label}</a>)}</nav>
    <main>{url.pathname === '/overview' ? <Operations /> : url.pathname === '/providers' ? <Operations providersOnly /> : url.pathname === '/' ? <History key={location} search={url.search} /> : match ? <Detail key={match[1]} id={match[1]} search={url.search} /> : <div className="notice"><h1>Page not found</h1><a href="/">Return to request history</a></div>}</main>
    <footer>Operational metadata only · No prompts or responses · Infrastructure trends remain in Grafana</footer>
  </div>;
}
