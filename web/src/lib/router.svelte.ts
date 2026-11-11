// Hash routing (#/devices) so the embedded server needs no SPA fallback. A page can keep
// its own state after a "?" (#/search?q=deny&range=1h), so it can be bookmarked or shared.

export type Route = 'welcome' | 'dashboard' | 'devices' | 'search' | 'export' | 'settings' | 'about';

const ROUTES: Route[] = ['welcome', 'dashboard', 'devices', 'search', 'export', 'settings', 'about'];

function parse(): { route: Route | null; query: string } {
  const h = location.hash.replace(/^#\/?/, '');
  const [path, ...rest] = h.split('?');
  const query = rest.join('?');
  if (path === '') return { route: null, query };
  return { route: (ROUTES as string[]).includes(path) ? (path as Route) : 'dashboard', query };
}

const first = parse();

export const router = $state<{ route: Route; explicit: boolean; query: string }>({
  route: first.route ?? 'dashboard',
  explicit: first.route !== null,
  query: first.query,
});

addEventListener('hashchange', () => {
  const p = parse();
  router.route = p.route ?? 'dashboard';
  router.query = p.query;
  router.explicit = true;
});

export function go(r: Route, query = ''): void {
  location.hash = `#/${r}${query ? `?${query}` : ''}`;
}

/** Rewrites the current page's query in the URL without a navigation or history entry. */
export function setQuery(query: string): void {
  const url = `#/${router.route}${query ? `?${query}` : ''}`;
  if (location.hash !== url) history.replaceState(history.state, '', url);
  router.query = query;
}

export const href = (r: Route) => `#/${r}`;
