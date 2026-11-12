<script lang="ts">
  import { ApiError } from '../lib/api';
  import { auth, loadSession, login } from '../lib/session.svelte';
  import { refreshDevices, refreshStatus } from '../lib/status.svelte';
  import Logo from '../components/Logo.svelte';

  let password = $state('');
  let busy = $state(false);
  let error = $state('');
  let lockedUntil = $state(0);
  let now = $state(Date.now());
  let input: HTMLInputElement | undefined = $state();
  // /api/status needs a login, so use the port this page came from (reset-password keeps ui.port).
  const port = location.port || (location.protocol === 'https:' ? '443' : '80');

  $effect(() => {
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });
  $effect(() => input?.focus());

  const wait = $derived(Math.max(0, Math.ceil((lockedUntil - now) / 1000)));
  const waitText = $derived(wait >= 60 ? `${Math.floor(wait / 60)} min ${wait % 60} s` : `${wait} s`);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (busy || wait > 0 || !password) return;
    busy = true;
    error = '';
    try {
      await login(password);
      password = '';
      lockedUntil = 0;
      refreshStatus();
      refreshDevices();
    } catch (x) {
      const status = x instanceof ApiError ? x.status : 0;
      if (status === 401) error = "That password isn't right.";
      else if (status === 429) {
        const secs = (x as ApiError).retryAfter || 1;
        lockedUntil = Date.now() + secs * 1000;
        now = Date.now();
        error = secs > 5 ? 'locked' : 'slow';
      } else if (status === 400) {
        await loadSession(); // the server isn't in LAN mode any more
      } else error = (x as Error).message;
    } finally {
      busy = false;
    }
  }
</script>

<div class="mx-auto mt-6 max-w-md">
  <form class="card space-y-4 p-7" onsubmit={submit} aria-labelledby="login-title">
    <div class="flex items-center gap-3">
      <span class="grid size-11 place-items-center rounded-full bg-chip shadow-[var(--shadow-chip)]"><Logo /></span>
      <div>
        <h1 id="login-title" class="m-0 text-xl font-bold tracking-tight">Log in to Woodshed</h1>
        <p class="m-0 text-[13px] text-muted">LAN mode is on, so this UI needs its password.</p>
      </div>
    </div>
    <label class="flex flex-col gap-1.5 text-[13.5px] font-medium">
      Password
      <input
        bind:this={input}
        class="field"
        type="password"
        autocomplete="current-password"
        bind:value={password}
        aria-invalid={!!error && error !== 'slow'}
        aria-describedby="login-error"
        disabled={wait > 0 && error === 'locked'}
      />
    </label>
    <div id="login-error" aria-live="polite" class="min-h-5 text-[13.5px] font-medium text-bad-fg">
      {#if error === 'locked'}
        {#if wait > 0}Too many wrong passwords. Logins from this address are locked for {waitText}.{:else}You can try again now.{/if}
      {:else if error === 'slow'}
        {#if wait > 0}Too many attempts in a row. Try again in {waitText}.{/if}
      {:else if error}{error}{/if}
      {#if auth.error && !error}Can't reach the Woodshed service ({auth.error}).{/if}
    </div>
    <button type="submit" class="pill-btn w-full justify-center py-2.5" disabled={busy || !password || wait > 0}>
      {busy ? 'Checking…' : 'Log in'}
    </button>
    <details class="text-[13px]">
      <summary class="cursor-pointer text-muted">Forgot the password?</summary>
      <p class="mt-2 mb-0 leading-relaxed">
        On the Woodshed machine, run <code class="font-mono">woodshed reset-password</code> as an administrator. It turns LAN mode off
        so the UI is back on <code class="font-mono whitespace-nowrap">http://localhost:{port}/</code>.
      </p>
    </details>
  </form>
</div>
