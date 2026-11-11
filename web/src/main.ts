import './app.css';
import './lib/theme.svelte';
import { mount } from 'svelte';
import App from './App.svelte';
import { setTransport } from './lib/api';

async function start() {
  // Mock mode: the build-time constant makes this branch, and the mock chunk, vanish from production builds.
  if (import.meta.env.VITE_MOCK) {
    const m = await import('./lib/mock');
    setTransport(m.mockTransport);
    m.mountPanel();
  }
  mount(App, { target: document.getElementById('app')! });
}

start();
