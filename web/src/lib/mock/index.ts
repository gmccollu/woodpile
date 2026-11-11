import { mount } from 'svelte';
import MockPanel from './MockPanel.svelte';
export { mockTransport } from './server.svelte';

export function mountPanel(): void {
  const el = document.createElement('div');
  document.body.appendChild(el);
  mount(MockPanel, { target: el });
}
