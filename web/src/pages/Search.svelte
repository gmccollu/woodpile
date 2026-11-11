<script lang="ts">
  import { ApiError, ALL_SEVS, getContext, search, type SearchParams, type SearchResult, type SevKey } from '../lib/api';
  import { bytes, num, stamp } from '../lib/format';
  import { matcher, parts } from '../lib/highlight';
  import { prefs } from '../lib/prefs.svelte';
  import { defaultRange, rangeFromQuery, rangeToQuery, resolveRange, type Range } from '../lib/range';
  import { go, router, setQuery } from '../lib/router.svelte';
  import { allScope, scopeDeviceIds, scopeFromQuery, scopeLabel, scopeParams, scopeToQuery, sevFromQuery, sevToQuery, type Scope } from '../lib/scope';
  import { sevClass, sevLabel } from '../lib/severity';
  import { deviceName, live } from '../lib/status.svelte';
  import { splitRaw } from '../lib/syslog';
  import DevicePicker from '../components/DevicePicker.svelte';
  import Icon from '../components/Icon.svelte';
  import SeverityChips from '../components/SeverityChips.svelte';
  import TimeRange from '../components/TimeRange.svelte';

  const PAGE = 500;
  const HOST_MAX = 28;

  // ----- the form -----
  let q = $state('');
  let regex = $state(false);
  let cs = $state(false);
  let scope = $state<Scope>(allScope());
  let range = $state<Range>(defaultRange());
  let sev = $state<SevKey[]>([...ALL_SEVS]);
  let order = $state<'desc' | 'asc'>('desc');
  let showRaw = $state(false);

  // ----- the search behind the results (frozen, so "Load more" sends the same parameters) -----
  let active = $state<SearchParams | null>(null);
  let activeScope = $state<Scope>(allScope());
  let results = $state.raw<SearchResult[]>([]);
  let seen = new Set<string>();
  let cursor = $state('');
  let status = $state<'idle' | 'running' | 'done' | 'cancelled' | 'error'>('idle');
  let error = $state('');
  let qError = $state('');
  let rangeError = $state('');
  let scanned = $state({ bytes: 0, files: 0 });
  let ctrl: AbortController | null = null;
  let gen = 0;

  function toQuery(): string {
    const p = new URLSearchParams();
    if (q) p.set('q', q);
    if (regex) p.set('re', '1');
    if (cs) p.set('cs', '1');
    scopeToQuery(scope, p);
    rangeToQuery(range, p);
    sevToQuery(sev, p);
    if (order === 'asc') p.set('order', 'asc');
    return p.toString();
  }

  function fromQuery(s: string) {
    const p = new URLSearchParams(s);
    q = p.get('q') ?? '';
    regex = p.get('re') === '1';
    cs = p.get('cs') === '1';
    scope = scopeFromQuery(p);
    range = rangeFromQuery(p);
    sev = sevFromQuery(p);
    order = p.get('order') === 'asc' ? 'asc' : 'desc';
  }

  // A bookmarked or shared link (#/search?q=…) runs straight away.
  let lastRun = '';
  $effect(() => {
    const s = router.query;
    if (s && s !== lastRun) {
      fromQuery(s);
      run();
    }
  });

  function cancel() {
    ctrl?.abort();
    gen++;
    if (status === 'running') status = 'cancelled';
  }

  async function run(e?: SubmitEvent) {
    e?.preventDefault();
    cancel();
    qError = rangeError = error = '';
    if (!sev.length) {
      error = 'Pick at least one severity.';
      status = 'error';
      return;
    }
    const { from, to } = resolveRange(range);
    if (!from || !to || !(Date.parse(from) < Date.parse(to))) {
      rangeError = '"From" must be before "To".';
      status = 'error';
      return;
    }
    lastRun = toQuery();
    setQuery(lastRun);
    activeScope = $state.snapshot(scope);
    active = { q, regex, case: cs, ...scopeParams(scope), from, to, severities: [...sev], order, limit: PAGE };
    results = [];
    seen = new Set();
    cursor = '';
    scanned = { bytes: 0, files: 0 };
    await fetchPage();
  }

  /** One page: keeps asking while the server says the scan was partial, until it's full or cancelled. */
  async function fetchPage() {
    if (!active) return;
    const my = ++gen;
    ctrl = new AbortController();
    status = 'running';
    try {
      for (;;) {
        const res = await search({ ...active, cursor: cursor || undefined }, ctrl.signal);
        if (my !== gen) return;
        const fresh = res.results.filter((r) => !seen.has(r.ref));
        for (const r of fresh) seen.add(r.ref);
        if (fresh.length) results = results.concat(fresh);
        cursor = res.next_cursor;
        scanned = { bytes: scanned.bytes + res.scanned_bytes, files: scanned.files + res.scanned_files };
        if (!(res.partial && res.next_cursor)) break;
      }
      status = 'done';
    } catch (x) {
      if (my !== gen || (x as Error).name === 'AbortError') return;
      status = 'error';
      const m = (x as Error).message;
      if (x instanceof ApiError && x.status === 400 && active.regex && /regexp|regular expression/i.test(m)) qError = m;
      else error = m;
    }
  }

  const hl = $derived(active ? matcher(active.q, !!active.regex, !!active.case) : null);
  const hostCh = $derived.by(() => {
    let w = 8;
    for (const id of new Set(results.map((r) => r.device_id))) w = Math.max(w, deviceName(id).length);
    return Math.min(w, HOST_MAX);
  });
  const text = (r: SearchResult) => (showRaw ? r.raw : splitRaw(r.raw).body);

  // ----- context panel -----
  interface Ctx {
    ref: string;
    before: number;
    after: number;
    lines: SearchResult[];
    hit: number;
    loading: boolean;
    error: string;
  }
  let ctx = $state<Ctx | null>(null);
  let ctxBox: HTMLDivElement | undefined = $state();

  async function openContext(ref: string, before = 50, after = 50) {
    const same = ctx?.ref === ref;
    ctx = { ref, before, after, lines: same ? ctx!.lines : [], hit: same ? ctx!.hit : -1, loading: true, error: '' };
    try {
      const res = await getContext(ref, before, after);
      if (ctx?.ref !== ref) return;
      ctx = { ...ctx, lines: res.lines, hit: res.hit_index, loading: false };
      if (!same) requestAnimationFrame(() => ctxBox?.querySelector('.hit')?.scrollIntoView({ block: 'center' }));
    } catch (x) {
      if (ctx?.ref === ref) ctx = { ...ctx, loading: false, error: (x as Error).message };
    }
  }
  const ctxHit = $derived(ctx && ctx.hit >= 0 ? ctx.lines[ctx.hit] : undefined);
  // Fewer lines than asked for means the stored log ends there.
  const ctxTop = $derived(!!ctx && ctx.hit >= 0 && ctx.hit < ctx.before);
  const ctxBottom = $derived(!!ctx && ctx.hit >= 0 && ctx.lines.length - ctx.hit - 1 < ctx.after);

  $effect(() => {
    if (!ctx) return;
    const esc = (e: KeyboardEvent) => {
      if (e.key === 'Escape') ctx = null;
    };
    addEventListener('keydown', esc);
    return () => removeEventListener('keydown', esc);
  });

  // ----- export these results -----
  function exportThese() {
    if (!active) return;
    const p = new URLSearchParams();
    p.set('dev', scopeDeviceIds(activeScope, live.devices ?? []).join(','));
    p.set('from', active.from!);
    p.set('to', active.to!);
    sevToQuery(active.severities ?? [...ALL_SEVS], p);
    if (active.q) p.set('q', active.q);
    go('export', p.toString());
  }

  function rowKey(e: KeyboardEvent, ref: string) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      openContext(ref);
    }
  }
</script>

<div class="space-y-4">
  <form class="card space-y-3" onsubmit={run} role="search" aria-label="Search stored logs">
    <div class="flex flex-wrap items-start gap-2">
      <div class="grow basis-80">
        <label class="sr-only" for="sq">Search text</label>
        <input
          id="sq"
          class="field font-mono"
          placeholder={regex ? 'Regular expression (RE2), e.g. deny (tcp|udp)' : 'Text to find, or leave empty to browse'}
          bind:value={q}
          spellcheck="false"
          autocomplete="off"
          aria-invalid={!!qError}
          aria-describedby="sq-err"
        />
        {#if qError}<p id="sq-err" class="ferr" role="alert">{qError}</p>{/if}
      </div>
      <button type="button" class="toggle" aria-pressed={regex} onclick={() => (regex = !regex)} title="Regular expression (RE2 syntax)">.* Regex</button>
      <button type="button" class="toggle" aria-pressed={cs} onclick={() => (cs = !cs)} title="Match upper and lower case exactly">Aa Case</button>
      <button type="submit" class="pill-btn px-5 py-2"><Icon name="search" size={15} />Search</button>
    </div>
    <div class="flex flex-wrap items-center gap-3">
      <DevicePicker bind:value={scope} devices={live.devices ?? []} />
      <TimeRange bind:value={range} error={rangeError} />
      <div class="seg" role="radiogroup" aria-label="Order">
        <button type="button" role="radio" aria-checked={order === 'desc'} onclick={() => (order = 'desc')}>Newest first</button>
        <button type="button" role="radio" aria-checked={order === 'asc'} onclick={() => (order = 'asc')}>Oldest first</button>
      </div>
    </div>
    <div class="sevbar"><SeverityChips bind:value={sev} label="Search severities" /></div>
  </form>

  <section class="log-panel res flex flex-col overflow-hidden" aria-label="Results">
    <div class="bar flex flex-wrap items-center gap-2 px-[18px] py-2.5 font-sans text-[13px]">
      <b class="text-white">Results</b>
      <span class="text-log-dim" aria-live="polite">
        {#if status === 'idle'}Search, or leave the box empty to browse.
        {:else if active}
          {num(results.length)} line{results.length === 1 ? '' : 's'}{cursor ? '+' : ''} · {scopeLabel(activeScope, live.devices ?? [])} · {stamp(
            active.from!,
            prefs.utc,
          )} → {stamp(active.to!, prefs.utc)}{prefs.utc ? ' UTC' : ''}
        {/if}
      </span>
      <span class="grow"></span>
      {#if status === 'running'}
        <span class="scan" role="status"><span class="spin" aria-hidden="true"></span>Scanned {bytes(scanned.bytes)} in {num(scanned.files)} file{scanned.files === 1 ? '' : 's'}…</span>
        <button type="button" class="tail-btn" onclick={cancel}>Cancel</button>
      {:else if status !== 'idle'}
        <span class="text-log-dim">Scanned {bytes(scanned.bytes)} in {num(scanned.files)} file{scanned.files === 1 ? '' : 's'}</span>
      {/if}
      <button type="button" class="tail-btn" aria-pressed={prefs.wrap} onclick={() => (prefs.wrap = !prefs.wrap)}>Wrap</button>
      <button type="button" class="tail-btn" aria-pressed={showRaw} onclick={() => (showRaw = !showRaw)} title="Show the PRI and syslog header too">Raw</button>
      <button
        type="button"
        class="tail-btn"
        onclick={exportThese}
        disabled={!active || !results.length || !!active.regex}
        title={active?.regex ? 'Exports filter on plain text only, not regular expressions' : 'Open Export with these devices, range, severities and text'}
      >
        <Icon name="export" size={14} />Export these results
      </button>
    </div>

    <div class="logview res-body overflow-auto px-[18px] py-2" class:nowrap={!prefs.wrap} style:--hostw="{hostCh}ch" style:--tw="18ch">
      {#if error}
        <p class="m-0 py-6 text-center font-sans text-[var(--sev-err)]" role="alert">{error}</p>
      {:else if qError}
        <p class="m-0 py-6 text-center font-sans text-log-dim">Fix the regular expression above and search again.</p>
      {:else if status === 'running' && !results.length}
        <p class="m-0 py-6 text-center font-sans text-log-dim">Searching…</p>
      {:else if (status === 'done' || status === 'cancelled') && !results.length}
        <p class="m-0 py-6 text-center font-sans text-log-dim">
          {status === 'cancelled' ? 'Cancelled before anything matched.' : 'No matches in this range. Try a longer range, fewer filters, or other words.'}
        </p>
      {/if}
      {#each results as r (r.ref)}
        <div
          class="logline clickable"
          role="button"
          tabindex="0"
          data-ref={r.ref}
          title={prefs.wrap ? 'Show the lines around this one' : r.raw}
          onclick={() => openContext(r.ref)}
          onkeydown={(e) => rowKey(e, r.ref)}
        >
          <span class="c t">{stamp(r.received, prefs.utc)}</span><span class="c h">{deviceName(r.device_id)}</span><span class="c s {sevClass(r.severity)}"
            >{sevLabel(r.severity)}</span
          ><span>{#each parts(text(r), hl) as p, i (i)}{#if p.hit}<mark class="hl">{p.t}</mark>{:else}{p.t}{/if}{/each}</span>
        </div>
      {/each}
      {#if results.length && cursor}
        <div class="py-3 text-center font-sans">
          {#if status === 'running'}
            <span class="text-log-dim">Loading…</span>
          {:else}
            <button type="button" class="tail-btn" onclick={fetchPage}>{status === 'cancelled' ? 'Continue the scan' : `Load ${num(PAGE)} more`}</button>
          {/if}
        </div>
      {:else if results.length && status === 'done'}
        <p class="m-0 py-3 text-center font-sans text-[12.5px] text-log-dim">End of the range.</p>
      {/if}
    </div>
  </section>
</div>

{#if ctx}
  <div class="overlay" role="presentation" onclick={() => (ctx = null)}></div>
  <div class="ctx log-panel" role="dialog" aria-modal="true" aria-label="Lines around the result">
    <div class="bar flex flex-wrap items-center gap-2 px-[18px] py-3 font-sans text-[13px]">
      <b class="text-white">Context</b>
      {#if ctxHit}<span class="text-log-dim">{deviceName(ctxHit.device_id)} · {stamp(ctxHit.received, prefs.utc)} · {ctx.before} before, {ctx.after} after</span>{/if}
      <span class="grow"></span>
      <button type="button" class="tail-btn" aria-pressed={prefs.wrap} onclick={() => (prefs.wrap = !prefs.wrap)}>Wrap</button>
      <button type="button" class="tail-btn" onclick={() => (ctx = null)} aria-label="Close (Esc)"><Icon name="x" size={14} /></button>
    </div>
    <div bind:this={ctxBox} class="logview min-h-0 grow overflow-auto px-[18px] py-2" class:nowrap={!prefs.wrap} style:--hostw="{hostCh}ch" style:--tw="18ch">
      {#if ctx.error}<p class="font-sans text-[var(--sev-err)]" role="alert">{ctx.error}</p>{/if}
      <div class="py-1 text-center font-sans">
        <button
          type="button"
          class="tail-btn"
          disabled={ctx.loading || ctx.before >= 500 || ctxTop}
          onclick={() => ctx && openContext(ctx.ref, Math.min(500, ctx.before + 50), ctx.after)}
        >
          {ctxTop ? 'Start of the stored log' : 'Show 50 more above'}
        </button>
      </div>
      {#if ctx.loading && !ctx.lines.length}<p class="font-sans text-log-dim">Loading…</p>{/if}
      {#each ctx.lines as l, i (l.ref)}
        <div class="logline" class:hit={i === ctx.hit} title={prefs.wrap ? undefined : l.raw}>
          <span class="c t">{stamp(l.received, prefs.utc)}</span><span class="c h">{deviceName(l.device_id)}</span><span class="c s {sevClass(l.severity)}"
            >{sevLabel(l.severity)}</span
          ><span>{#each parts(text(l), i === ctx.hit ? hl : null) as p, j (j)}{#if p.hit}<mark class="hl">{p.t}</mark>{:else}{p.t}{/if}{/each}</span>
        </div>
      {/each}
      <div class="py-1 text-center font-sans">
        <button
          type="button"
          class="tail-btn"
          disabled={ctx.loading || ctx.after >= 500 || ctxBottom}
          onclick={() => ctx && openContext(ctx.ref, ctx.before, Math.min(500, ctx.after + 50))}
        >
          {ctxBottom ? 'Newest stored line' : 'Show 50 more below'}
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  .toggle {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 8px 12px;
    border-radius: 12px;
    border: 1px solid var(--input-border);
    background: var(--input);
    color: var(--fg);
    font-size: 13px;
    font-family: var(--font-mono);
    cursor: pointer;
    white-space: nowrap;
  }
  .toggle[aria-pressed='true'] {
    background: var(--btn);
    color: var(--btn-fg);
    border-color: var(--btn);
  }
  .seg {
    display: inline-flex;
    background: var(--glass);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 3px;
  }
  .seg button {
    border: 0;
    background: transparent;
    color: var(--fg);
    padding: 5px 12px;
    border-radius: 999px;
    font-size: 13px;
    cursor: pointer;
  }
  .seg button[aria-checked='true'] {
    background: var(--chip);
    font-weight: 600;
    box-shadow: var(--shadow-chip);
  }
  .sevbar {
    display: inline-flex;
    padding: 6px 10px;
    border-radius: 14px;
    background: var(--log-bar);
    border: 1px solid var(--log-border);
  }
  .ferr {
    margin: 4px 0 0;
    font-size: 13px;
    font-weight: 500;
    color: var(--bad-fg);
    font-family: var(--font-mono);
  }
  .res {
    border-radius: 22px;
  }
  .res-body {
    max-height: calc(100vh - 160px);
    min-height: 240px;
  }
  .bar {
    background: var(--log-bar);
    border-bottom: 1px solid rgba(255, 255, 255, 0.1);
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
  .tail-btn:hover:not(:disabled),
  .tail-btn[aria-pressed='true'] {
    background: rgba(255, 255, 255, 0.12);
  }
  .tail-btn:disabled {
    opacity: 0.45;
    cursor: default;
  }
  .scan {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    color: var(--sev-warn);
  }
  .spin {
    width: 12px;
    height: 12px;
    border-radius: 50%;
    border: 2px solid rgba(255, 255, 255, 0.2);
    border-top-color: var(--sev-warn);
    animation: spin 0.9s linear infinite;
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
  .overlay {
    position: fixed;
    inset: 0;
    z-index: 50;
    background: rgba(10, 12, 16, 0.45);
  }
  .ctx {
    position: fixed;
    z-index: 51;
    top: 24px;
    right: 24px;
    bottom: 24px;
    width: min(1200px, calc(100vw - 48px));
    display: flex;
    flex-direction: column;
    border-radius: 22px;
    overflow: hidden;
    box-shadow: 0 30px 80px rgba(0, 0, 0, 0.45);
  }
</style>
