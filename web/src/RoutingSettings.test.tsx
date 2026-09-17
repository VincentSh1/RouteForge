import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { expect, it, vi } from 'vitest';
import { RoutingSettings } from './RoutingSettings';
import { APIError, operationsAPI, routingAPI } from './api';
import type { Overview } from './api';

const initial = { policy: 'deterministic', exploration_interval: 10, max_latency_over_fastest_percent: null };
const overview: Overview = { observed_at: '', features: { cache: true, persistence: true, metrics: true, tracing: false }, providers: [],
 routing: { policy: 'deterministic', auto: false, provider_order: ['mock'], latency_aware: false, min_samples: 5, sample_max_age_us: 300000000, exploration_interval: 10, exploration_counts: null, max_latency_over_fastest_percent: null } };
function setup() {
 vi.spyOn(routingAPI,'get').mockResolvedValue(initial);
 vi.spyOn(operationsAPI,'overview').mockResolvedValue(overview);
 vi.spyOn(window,'confirm').mockReturnValue(true);
 return vi.spyOn(routingAPI,'save').mockResolvedValue(initial);
}
it('renders current settings and validates before saving', async()=>{
 const save=setup(); render(<RoutingSettings/>);
 expect(screen.getByText('Loading routing settings…')).toBeInTheDocument();
 const select=await screen.findByLabelText('Routing policy');
 expect(select).toHaveValue('deterministic');
 fireEvent.change(select,{target:{value:'cost_latency'}});
 fireEvent.click(screen.getByRole('button',{name:'Save'}));
 expect(await screen.findByRole('alert')).toHaveTextContent('requires a tolerance');
 expect(save).not.toHaveBeenCalled();
});
it('refreshes authoritative settings and operations after save', async()=>{
 const save=setup(); render(<RoutingSettings/>);
 const select=await screen.findByLabelText('Routing policy');
 fireEvent.change(select,{target:{value:'cost'}});
 vi.mocked(routingAPI.get).mockResolvedValue({...initial,policy:'cost'});
 fireEvent.click(screen.getByRole('button',{name:'Save'}));
 expect(await screen.findByText('Saved. Current server state refreshed.')).toBeInTheDocument();
 expect(save).toHaveBeenCalledWith({...initial,policy:'cost'},expect.any(AbortSignal));
 expect(operationsAPI.overview).toHaveBeenCalledTimes(2);
 expect(routingAPI.get).toHaveBeenCalledTimes(2);
});
it('does not claim a failed save succeeded and allows retry',async()=>{
 const save=setup(); save.mockRejectedValue(new APIError(503)); render(<RoutingSettings/>);
 fireEvent.change(await screen.findByLabelText('Routing policy'),{target:{value:'cost'}});
 fireEvent.click(screen.getByRole('button',{name:'Save'}));
 expect(await screen.findByRole('alert')).toHaveTextContent('Could not save');
 expect(screen.queryByText('Saved. Current server state refreshed.')).not.toBeInTheDocument();
 fireEvent.click(screen.getByRole('button',{name:'Retry'}));
 await waitFor(()=>expect(routingAPI.get).toHaveBeenCalledTimes(2));
});
it('signals an expired session without exposing response errors', async()=>{
 const listener=vi.fn(); window.addEventListener('routeforge-session-expired',listener);
 vi.stubGlobal('fetch',vi.fn().mockResolvedValue(new Response('{}',{status:401})));
 await expect(routingAPI.save(initial,new AbortController().signal)).rejects.toBeInstanceOf(APIError);
 expect(listener).toHaveBeenCalledTimes(1);
 window.removeEventListener('routeforge-session-expired',listener);
});
