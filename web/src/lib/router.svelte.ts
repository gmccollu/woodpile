// Hash routing (#/devices) so the embedded server needs no SPA fallback.

export type Route = 'welcome' | 'dashboard' | 'devices' | 'search' | 'export' | 'settings' | 'about';

const ROUTES: Route[] = ['welcome', 'dashboard', 'devices', 'search', 'export', 'settings', 'about'];

function parse(): Route | null {
  const h = location.hash.replace(/^#\/?/, '').split('?')[0];
  if (h === '') return null;
  return (ROUTES as string[]).includes(h) ? (h as Route) : 'dashboard';
}

export const router = $state<{ route: Route; explicit: boolean }>({
  route: parse() ?? 'dashboard',
  explicit: parse() !== null,
});

addEventListener('hashchange', () => {
  router.route = parse() ?? 'dashboard';
  router.explicit = true;
});

export function go(r: Route): void {
  location.hash = `#/${r}`;
}

export const href = (r: Route) => `#/${r}`;
