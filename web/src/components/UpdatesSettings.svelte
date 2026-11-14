<script lang="ts">
  // Settings → Updates (docs/api.md → Update check): the switch, the file it fetches, the last
  // and next check, and Check now. The switch saves at once (PUT /api/updates), like the
  // banner's Turn on / No thanks, outside the page's Save button.
  import { ApiError, checkUpdatesNow, getUpdates, putUpdates, type UpdateInfo } from '../lib/api';
  import { ago, dateTime } from '../lib/format';
  import { live, refreshStatus } from '../lib/status.svelte';

  /** Called after the switch saved (the settings' ETag changed). */
  let { onchange }: { onchange?: () => void } = $props();

  let info = $state<UpdateInfo | null>(null);
  let loadError = $state('');
  let busy = $state(false);
  let message = $state('');
  /** Set by Check now until last_check moves past it. */
  let checkingSince = $state<string | null>(null);

  async function load() {
    try {
      info = await getUpdates();
      loadError = '';
      if (checkingSince !== null && info.last_check !== checkingSince) checkingSince = null;
    } catch (e) {
      loadError = (e as Error).message;
    }
  }

  $effect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  });

  async function setCheck(on: boolean) {
    busy = true;
    message = '';
    try {
      info = await putUpdates(on ? 'on' : 'off');
      onchange?.();
      await refreshStatus();
    } catch (e) {
      message = (e as Error).message;
    } finally {
      busy = false;
    }
  }

  async function checkNow() {
    if (!info) return;
    busy = true;
    message = '';
    try {
      checkingSince = info.last_check ?? '';
      await checkUpdatesNow();
      setTimeout(load, 1500);
    } catch (e) {
      checkingSince = null;
      message =
        e instanceof ApiError && e.status === 429
          ? `It checked less than a minute ago. Try again in ${e.retryAfter || 60} s.`
          : (e as Error).message;
    } finally {
      busy = false;
    }
  }

  const on = $derived(info?.check === 'on');
  const when = (iso: string) => {
    const a = ago(iso, live.now);
    return a === 'now' ? 'just now' : a;
  };
</script>

<section class="card" aria-labelledby="updates">
  <h2 class="sec" id="updates">Updates</h2>
  {#if loadError && !info}
    <p class="hint mt-0" role="status">Couldn't read the update check: {loadError}</p>
  {:else if info && !info.available}
    <p class="hint mt-0">
      Not available in this build ({info.current}). Release builds can check once a day for a newer version; this one never connects out.
    </p>
  {:else if info}
    <label class="flex items-start gap-3">
      <input type="checkbox" checked={on} disabled={busy} onchange={(e) => setCheck(e.currentTarget.checked)} aria-describedby="hint-updates" class="mt-1" />
      <span>
        <span class="font-semibold">Check for new versions once a day</span>
        <span class="hint block" id="hint-updates">
          Woodshed fetches one public file, <code class="font-mono text-[12.5px] break-all">{info.url}</code>, and sends nothing about this server. If a newer
          version is out, a banner links to its release page. It never downloads or installs anything.
          {#if info.check === 'ask'}<br />Not decided yet: the banner at the top asks once.{/if}
        </span>
      </span>
    </label>

    {#if on}
      <dl class="facts">
        <dt>Last check</dt>
        <dd data-testid="update-last">
          {#if checkingSince !== null}
            Checking…
          {:else if !info.last_check}
            Not yet.
          {:else if info.last_result === 'ok' && info.update_available && info.latest}
            Woodshed {info.latest.version} is available (this is {info.current}), checked {when(info.last_check)}.
            <a class="text-link underline" href={info.latest.url} target="_blank" rel="noopener noreferrer">Release page</a>
          {:else if info.last_result === 'ok' && info.latest}
            OK, {info.latest.version} is the latest (checked {when(info.last_check)}).
          {:else}
            <span class="bad">Last check failed {when(info.last_check)}: {info.last_error}</span>
          {/if}
        </dd>
        <dt>Next check</dt>
        <dd>{info.next_check ? dateTime(info.next_check) : 'Not scheduled.'}</dd>
      </dl>
    {/if}
    <div class="mt-3 flex flex-wrap items-center gap-3">
      <button type="button" class="ghost-btn" onclick={checkNow} disabled={busy || !on || checkingSince !== null}>Check now</button>
      {#if message}<span class="hint" role="status">{message}</span>{/if}
    </div>
  {/if}
</section>

<style>
  /* As Settings.svelte's (its styles are scoped to that page). */
  .sec {
    margin: 0 0 6px;
    font-size: 17px;
    font-weight: 700;
  }
  .hint {
    font-size: 13px;
    color: var(--muted);
    margin-bottom: 10px;
  }
  .facts {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 6px 16px;
    margin: 14px 0 0;
    font-size: 14px;
  }
  .facts dt {
    color: var(--muted);
  }
  .facts dd {
    margin: 0;
  }
  .bad {
    color: var(--bad-fg);
  }
</style>
