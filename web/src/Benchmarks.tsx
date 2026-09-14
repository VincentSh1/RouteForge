import { useEffect, useState } from 'react';
import { benchmarkAPI } from './api';
import type { BenchmarkComparison, BenchmarkScenario, BenchmarkState } from './api';
import { cost, tokens } from './format';

const milliseconds = (value: number | undefined) => value === undefined ? '—' : tokens(value) + ' ms';
const percent = (value: number) => (value * 100).toFixed(1) + '%';
const selections = (value: Record<string, number>) => Object.entries(value).sort(([a], [b]) => a.localeCompare(b)).map(([name, count]) => name + ': ' + count).join(' · ') || '—';

export function Benchmarks() {
  const [catalog, setCatalog] = useState<BenchmarkScenario[] | null>(null);
  const [scenario, setScenario] = useState('');
  const [state, setState] = useState<BenchmarkState>('warm');
  const [result, setResult] = useState<BenchmarkComparison | null>(null);
  const [catalogError, setCatalogError] = useState(false);
  const [error, setError] = useState(false);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setCatalogError(false);
    benchmarkAPI.scenarios(controller.signal).then(value => {
      if (!controller.signal.aborted) { setCatalog(value.scenarios); setScenario(current => current || value.scenarios[0]?.id || ''); }
    }).catch(() => { if (!controller.signal.aborted) setCatalogError(true); });
    return () => controller.abort();
  }, [revision]);
  useEffect(() => {
    const controller = new AbortController();
    setResult(null); setError(false);
    if (scenario) benchmarkAPI.compare(scenario, state, controller.signal).then(value => {
      if (!controller.signal.aborted) setResult(value);
    }).catch(() => { if (!controller.signal.aborted) setError(true); });
    return () => controller.abort();
  }, [scenario, state, revision]);
  const selected = catalog?.find(item => item.id === scenario);
  const visible = result?.scenario === scenario && result.state === state ? result : null;
  return <>
    <div className="heading"><div><p className="eyebrow">OFFLINE POLICY COMPARISON</p><h1>Benchmarks</h1>
      <p className="muted">Controlled synthetic scenarios, not production traffic or real provider performance.</p></div></div>
    <p className="notice">System performance and configured economics only. These results do not measure semantic quality, correctness, or human preference. Every policy receives identical counterfactual provider conditions in its own isolated gateway.</p>
    {catalog && <div className="benchmark-controls"><label>Scenario<select value={scenario} onChange={event => setScenario(event.target.value)}>{catalog.map(item => <option key={item.id} value={item.id}>{item.id.replaceAll('_', ' ')} · {item.mode.replaceAll('_', ' ')}</option>)}</select></label>
      <label>Initial state<select value={state} onChange={event => setState(event.target.value as BenchmarkState)}><option value="warm">Warm — explicit warm-up replay</option><option value="cold">Cold — empty telemetry</option></select></label></div>}
    <p className="hint">Warm-up requests are excluded from measured results. Cold runs begin with empty telemetry. No live routing configuration is changed.</p>
    {catalogError || error ? <div className="notice error" role="alert"><strong>Benchmark unavailable</strong><p>Check the local console connection and retry.</p><button onClick={() => setRevision(n => n + 1)}>Retry</button></div>
      : catalog?.length === 0 ? <p className="notice">No built-in scenarios available.</p>
      : !visible ? <p className="notice" role="status">Loading benchmark comparison…</p> : <>
        <div className="section-heading"><h2>{visible.scenario.replaceAll('_', ' ')} / {visible.state}</h2><span>Fixture v{visible.scenario_version} · {selected?.requests} measured requests per policy · {state === 'warm' ? selected?.warmup_requests : 0} warm-up requests</span></div>
        <div className="table-wrap"><table aria-label="Policy performance comparison"><thead><tr><th>Policy</th><th>Success</th><th>Completion p50 / p95</th><th>Streaming TTFC p50 / p95</th><th>Estimated configured cost</th><th>Cost / successful request</th></tr></thead>
          <tbody>{visible.results.map(row => <tr key={row.policy}><th scope="row">{row.policy}</th><td>{percent(row.success_rate)}</td><td>{milliseconds(row.p50_latency_ms)} / {milliseconds(row.p95_latency_ms)}</td><td>{milliseconds(row.p50_ttfc_ms)} / {milliseconds(row.p95_ttfc_ms)}</td><td>{cost(row.estimated_cost_micro_usd)}</td><td>{cost(row.estimated_cost_per_successful_request_micro_usd ?? null)}</td></tr>)}</tbody></table></div>
        <p className="hint">Compare cost alongside latency and success, not as a combined score. — means unavailable/not applicable, not zero. Completion includes failed requests and fallback time; TTFC includes only streams that reached first content, including later failures. Percentiles use nearest-rank. Streaming lifetime is not TTFC.</p>
        <div className="section-heading"><h2>Routing behavior</h2></div>
        <div className="table-wrap"><table aria-label="Policy routing comparison"><thead><tr><th>Policy</th><th>Attempts / request</th><th>Fallbacks / request</th><th>Requests with fallback</th><th>Initial selections</th><th>Selection switches</th></tr></thead><tbody>{visible.results.map(row => <tr key={row.policy}><th scope="row">{row.policy}</th><td>{row.average_attempts_per_request.toFixed(2)}</td><td>{row.requests ? (Object.values(row.fallback_provider_attempts).reduce((sum, value) => sum + value, 0) / row.requests).toFixed(2) : '—'}</td><td>{percent(row.fallback_rate)}</td><td>{selections(row.initial_provider_selections)}</td><td>{tokens(row.provider_selection_switches)}</td></tr>)}</tbody></table></div>
        <p className="hint">Fallbacks count additional actual attempts. Cost sums available configured estimates across all attempts, including failed/fallback attempts; unavailable estimates can undercount cost. Cost per success includes failed-request costs, not just successful attempts. These are synthetic prices, not provider invoices. Results are retained only in process memory; no benchmark history is stored.</p>
      </>}
  </>;
}
