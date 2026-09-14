import { useEffect, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';

export interface Session { enabled: boolean; authenticated: boolean }

async function sessionRequest(action: 'session' | 'login' | 'logout', secret?: string): Promise<Session> {
  const response = await fetch('/api/auth/' + action, {
    method: action === 'session' ? 'GET' : 'POST', credentials: 'same-origin', cache: 'no-store',
    signal: AbortSignal.timeout(10000),
    ...(action === 'session' ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(action === 'login' ? { secret } : {}) }),
  });
  if (!response.ok) throw new Error('Authentication unavailable');
  const value: unknown = await response.json();
  if (value === null || typeof value !== 'object' || !('authenticated' in value) || typeof value.authenticated !== 'boolean') throw new Error('Authentication unavailable');
  if (action === 'session' && (!('enabled' in value) || typeof value.enabled !== 'boolean')) throw new Error('Authentication unavailable');
  return { enabled: action === 'session' && 'enabled' in value ? value.enabled === true : true, authenticated: value.authenticated };
}
export const authAPI = {
  session: () => sessionRequest('session'),
  login: (secret: string) => sessionRequest('login', secret),
  logout: () => sessionRequest('logout'),
};

export function Auth({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<Session | null>(null);
  const [error, setError] = useState(false);
  const [busy, setBusy] = useState(false);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    let active = true;
    setError(false);
    const expired = () => { active = false; setSession({ enabled: true, authenticated: false }); setError(false); };
    window.addEventListener('routeforge-session-expired', expired);
    authAPI.session().then(value => { if (active) setSession(value); }).catch(() => { if (active) setError(true); });
    return () => { active = false; window.removeEventListener('routeforge-session-expired', expired); };
  }, [revision]);
  async function login(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const secret = String(new FormData(form).get('secret') ?? '');
    form.reset(); setBusy(true); setError(false);
    try { setSession(await authAPI.login(secret)); } catch { setError(true); } finally { setBusy(false); }
  }
  async function logout() {
    setBusy(true); setError(false);
    try { setSession(await authAPI.logout()); } catch { setError(true); } finally { setBusy(false); }
  }
  if (!session) return <div className="app"><h1>RouteForge Console</h1>{error ? <div role="alert"><p>Session check unavailable. Access remains locked.</p><button onClick={() => setRevision(n => n + 1)}>Retry</button></div> : <p role="status">Checking session…</p>}</div>;
  if (session.authenticated) return <>{session.enabled && <div className="session-bar"><span>Local admin session</span><button disabled={busy} onClick={logout}>Logout</button>{error && <p role="alert">Logout could not be confirmed. Retry Logout.</p>}</div>}{children}</>;
  return <div className="app login"><p className="eyebrow">LOCAL CONTROL PLANE</p><h1>Login to RouteForge</h1><p className="muted">Enter the configured admin secret. Inference does not require this login.</p>
    <form onSubmit={login}><label>Admin secret<input name="secret" type="password" autoComplete="off" required maxLength={256} disabled={busy} /></label>
      <button className="primary" disabled={busy}>Login</button></form>
    {busy && <p role="status">Signing in…</p>}{error && <p role="alert">Login failed or temporarily unavailable. Check your secret and retry.</p>}
    <p className="hint">Credentials are not saved in browser storage. Sessions expire and are invalidated when RouteForge restarts.</p></div>;
}
