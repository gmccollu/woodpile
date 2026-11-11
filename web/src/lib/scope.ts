// Which devices a search or export covers: all, a group, the ungrouped ones, or a list.
import type { SevKey } from './api';
import { ALL_SEVS } from './api';
import { deviceName } from './status.svelte';

export interface Scope {
  mode: 'all' | 'group' | 'ungrouped' | 'devices';
  group: string;
  devices: string[];
}

export const allScope = (): Scope => ({ mode: 'all', group: '', devices: [] });

/** API parameters for search (docs/api.md: devices, group / ungrouped). */
export function scopeParams(s: Scope): { devices?: string[]; group?: string; ungrouped?: boolean } {
  if (s.mode === 'devices' && s.devices.length) return { devices: s.devices };
  if (s.mode === 'group' && s.group) return { group: s.group };
  if (s.mode === 'ungrouped') return { ungrouped: true };
  return {};
}

export function scopeLabel(s: Scope): string {
  if (s.mode === 'group') return `Group: ${s.group}`;
  if (s.mode === 'ungrouped') return 'Ungrouped devices';
  if (s.mode === 'devices') {
    if (!s.devices.length) return 'No devices';
    const names = s.devices.slice(0, 3).map(deviceName);
    return s.devices.length > 2 ? `${names.slice(0, 2).join(', ')} +${s.devices.length - 2}` : names.join(', ');
  }
  return 'All devices';
}

export function scopeToQuery(s: Scope, q: URLSearchParams): void {
  if (s.mode === 'devices' && s.devices.length) q.set('dev', s.devices.join(','));
  else if (s.mode === 'group' && s.group) q.set('group', s.group);
  else if (s.mode === 'ungrouped') q.set('ug', '1');
}

export function scopeFromQuery(q: URLSearchParams): Scope {
  const dev = q.get('dev');
  if (dev) return { mode: 'devices', group: '', devices: dev.split(',').filter(Boolean) };
  if (q.get('group')) return { mode: 'group', group: q.get('group')!, devices: [] };
  if (q.get('ug') === '1') return { mode: 'ungrouped', group: '', devices: [] };
  return allScope();
}

/** Severity selection in a URL: omitted = all, "none" = nothing. */
export function sevToQuery(sel: SevKey[], q: URLSearchParams): void {
  if (sel.length === ALL_SEVS.length) return;
  q.set('sev', sel.length ? sel.join(',') : 'none');
}

export function sevFromQuery(q: URLSearchParams): SevKey[] {
  const v = q.get('sev');
  if (!v) return [...ALL_SEVS];
  if (v === 'none') return [];
  const parts = v.split(',');
  return ALL_SEVS.filter((k) => parts.includes(String(k)));
}
