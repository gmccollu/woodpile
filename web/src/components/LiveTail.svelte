<script lang="ts">
  import { tick } from 'svelte';
  import { openLive, type LogLine, type SampledNotice } from '../lib/api';
  import { clock, num } from '../lib/format';
  import { SEVERITIES, sevClass, sevLabel } from '../lib/severity';
  import { deviceName } from '../lib/status.svelte';
  import { splitRaw } from '../lib/syslog';
  import Icon from './Icon.svelte';

  let {
    group = '',
    ungrouped = false,
    devices = [],
    q = '',
    scope = 'all devices',
    onClearScope,
    height = '560px',
  }: {
    group?: string;
    ungrouped?: boolean;
    devices?: string[];
    q?: string;
    scope?: string;
    onClearScope?: () => void;
    height?: string;
  } = $props();

  /** Lines kept in the DOM. Older lines drop off the top. */
  const MAX = 1000;

  interface Row extends LogLine {
    k: number;
    pri: string;
    header: string;
    body: string;
  }

  let minSev = $state(7);
  let paused = $state(false);
  /** Show each line exactly as received, PRI and header included. */
  let showRaw = $state(false);
  let rows = $state.raw<Row[]>([]);
  let heldCount = $state(0);
  let sampled = $state<SampledNotice | null>(null);
  let conn = $state<'connecting' | 'open' | 'error' | 'closed'>('connecting');
  /** Bumped to reopen a stream the browser closed for good (it won't retry after a 401 or 5xx). */
  let attempt = $state(0);
  let qDebounced = $state('');
  // A string key, so a new array with the same ids doesn't reconnect.
  const devKey = $derived(devices.join(','));
  let scroller: HTMLDivElement | undefined = $state();

  let pending: Row[] = [];
  let seq = 0;
  let flushTimer = 0;
  let reopen = 0;
  let sampledAt = 0;

  $effect(() => {
    const v = q;
    const t = setTimeout(() => (qDebounced = v.trim()), 300);
    return () => clearTimeout(t);
  });

  function toRow(l: LogLine): Row {
    return { ...l, k: seq++, ...splitRaw(l.raw) };
  }

  async function flush() {
    flushTimer = 0;
    if (paused || !pending.length) return;
    const el = scroller;
    const stick = !el || el.scrollHeight - el.scrollTop - el.clientHeight < 48;
    const next = rows.length + pending.length > MAX ? rows.concat(pending).slice(-MAX) : rows.concat(pending);
    pending = [];
    rows = next;
    if (stick && el) {
      await tick();
      el.scrollTop = el.scrollHeight;
    }
  }

  function schedule() {
    if (!flushTimer && !paused) flushTimer = window.setTimeout(flush, 100);
  }

  // Reconnect whenever a filter changes. The server applies the filters and replays
  // up to 200 matching lines from its ring buffer on connect.
  $effect(() => {
    void attempt;
    const filters = { group: group || undefined, ungrouped, devices: devKey ? devKey.split(',') : undefined, min_severity: minSev < 7 ? minSev : undefined, q: qDebounced || undefined };
    rows = [];
    pending = [];
    heldCount = 0;
    sampled = null;
    conn = 'connecting';
    const close = openLive(filters, {
      onMessage(l) {
        pending.push(toRow(l));
        if (pending.length > MAX) pending.splice(0, pending.length - MAX);
        if (paused) heldCount++;
        else schedule();
      },
      onSampled(s) {
        sampled = s;
        sampledAt = Date.now();
      },
      onState(s) {
        conn = s;
        if (s === 'closed') reopen = window.setTimeout(() => attempt++, 3000);
      },
    });
    const staleSample = setInterval(() => {
      if (sampled && Date.now() - sampledAt > 2500) sampled = null;
    }, 1000);
    return () => {
      close();
      clearInterval(staleSample);
      clearTimeout(flushTimer);
      clearTimeout(reopen);
      flushTimer = 0;
    };
  });

  function togglePause() {
    paused = !paused;
    if (!paused) {
      heldCount = 0;
      flush();
    }
  }

  function clear() {
    rows = [];
    pending = [];
    heldCount = 0;
  }
</script>

<section class="log-panel flex flex-col overflow-hidden rounded-[22px]" style:height aria-label="Live tail">
  <div class="flex flex-wrap items-center gap-x-2 gap-y-2 border-b border-white/10 px-[18px] py-3 font-sans text-[13px]" style:background="var(--log-bar)">
    <div class="flex min-w-0 grow basis-40 items-center gap-2">
      <span
        class="inline-block size-2 shrink-0 rounded-full"
        style:background={paused ? 'var(--sev-warn)' : conn === 'open' ? 'var(--sev-info)' : 'var(--sev-err)'}
        aria-hidden="true"
      ></span>
      <b class="text-white">Live tail</b>
      <span class="truncate text-log-dim">· {scope}{qDebounced ? ` · matching “${qDebounced}”` : ''}</span>
      {#if onClearScope}
        <button class="tail-btn" onclick={onClearScope} aria-label="Show all devices"><Icon name="x" size={13} /></button>
      {/if}
    </div>
    <label class="flex items-center">
      <span class="sr-only">Severity</span>
      <select bind:value={minSev} class="tail-select">
        {#each SEVERITIES as s (s.n)}
          <option value={s.n}>{s.n === 7 ? 'All severities' : `${s.short} and worse`}</option>
        {/each}
      </select>
    </label>
    <button class="tail-btn" onclick={togglePause} aria-pressed={paused}>
      <Icon name={paused ? 'play' : 'pause'} size={14} />{paused ? 'Resume' : 'Pause'}
    </button>
    <button class="tail-btn" onclick={() => (showRaw = !showRaw)} aria-pressed={showRaw} title="Show the PRI and syslog header too">Raw</button>
    <button class="tail-btn" onclick={clear}><Icon name="clear" size={14} />Clear</button>
  </div>

  <div class="h-7 shrink-0 truncate px-[18px] pt-1.5 font-sans text-[12.5px]" aria-live="polite">
    {#if paused}
      <p class="m-0 text-[var(--sev-warn)]">
        Paused{heldCount ? `: ${num(heldCount)} new line${heldCount === 1 ? '' : 's'} waiting` : ''}. Still receiving; Resume shows the latest.
      </p>
    {:else if sampled}
      <p class="m-0 text-[var(--sev-warn)]">{num(sampled.rate)} msg/s, showing a sample</p>
    {:else if conn === 'error' || conn === 'closed'}
      <p class="m-0 text-[var(--sev-err)]">Connection lost. Reconnecting…</p>
    {/if}
  </div>

  <!-- Focusable so keyboard users can scroll the log (scrollable regions must be reachable). -->
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <div bind:this={scroller} class="min-h-0 grow overflow-y-auto px-[18px] pb-3 pt-1 text-[12.5px] leading-[1.7]" role="log" aria-label="Log lines" tabindex="0">
    {#if !rows.length}
      <p class="m-0 py-6 text-center font-sans text-log-dim">
        {conn === 'connecting' ? 'Connecting…' : paused ? 'Paused.' : 'Waiting for messages…'}
      </p>
    {/if}
    {#each rows as r (r.k)}
      <div class="line">
        <span class="c t text-log-dim" title={r.received}>{clock(r.received)}</span><span class="c h" title={r.device_id}>{deviceName(r.device_id)}</span><span class="c s {sevClass(r.severity)}">{sevLabel(r.severity)}</span><span title={showRaw ? undefined : r.raw}>{#if showRaw}<span class="text-log-dim">{r.pri}{r.header}</span>{/if}{r.body}</span>
      </div>
    {/each}
  </div>
</section>

<style>
  /* Fixed-width time, host and severity on the first line; the message wraps under it
     with a small hanging indent, like a terminal, so long lines use the full width. */
  .line {
    padding-left: 3ch;
    text-indent: -3ch;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
  }
  .line > .c {
    display: inline-block;
    text-indent: 0;
    vertical-align: top;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    margin-right: 1.2ch;
  }
  .c.t {
    width: 12ch;
  }
  .c.h {
    width: 12ch;
    color: var(--log-host);
  }
  .c.s {
    width: 7ch;
    margin-right: 0.5ch;
  }
  .tail-btn {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    padding: 4px 10px;
    border-radius: 999px;
    border: 1px solid rgba(255, 255, 255, 0.18);
    background: transparent;
    color: var(--log-fg);
    cursor: pointer;
    font-size: 13px;
  }
  .tail-btn:hover,
  .tail-btn[aria-pressed='true'] {
    background: rgba(255, 255, 255, 0.1);
  }
  .tail-select {
    background: var(--log-bg);
    color: var(--log-fg);
    border: 1px solid rgba(255, 255, 255, 0.22);
    border-radius: 8px;
    padding: 2px 6px;
    font-size: 13px;
  }
  @media (max-width: 640px) {
    .c.s {
      display: none;
    }
  }
</style>
