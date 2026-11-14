<script lang="ts">
  // Matches per time bucket for the current query (SPEC §10.1.2). Dragging across it picks a
  // range snapped to bucket edges; a click picks one bucket. Buckets the scan hasn't reached
  // yet (count null) are hatched; buckets spread from the index's checkpoints say "≈" in
  // their tooltip only.
  import type { Histogram } from '../lib/api';
  import { snapSelection } from '../lib/events';
  import { num } from '../lib/format';

  let {
    hist,
    from,
    to,
    utc = false,
    busy = false,
    onselect,
  }: {
    hist: Histogram | null;
    from: string;
    to: string;
    utc?: boolean;
    busy?: boolean;
    onselect: (from: string, to: string) => void;
  } = $props();

  const pad = (n: number) => String(n).padStart(2, '0');
  function label(t: number, withDay: boolean, withSec: boolean): string {
    const d = new Date(t);
    const [mo, da, h, mi, s] = utc
      ? [d.getUTCMonth() + 1, d.getUTCDate(), d.getUTCHours(), d.getUTCMinutes(), d.getUTCSeconds()]
      : [d.getMonth() + 1, d.getDate(), d.getHours(), d.getMinutes(), d.getSeconds()];
    const day = `${pad(mo)}-${pad(da)}`;
    const time = `${pad(h)}:${pad(mi)}${withSec ? `:${pad(s)}` : ''}`;
    return withDay ? (h === 0 && mi === 0 ? day : `${day} ${time}`) : time;
  }

  const buckets = $derived(hist?.buckets ?? []);
  const size = $derived((hist?.bucket_seconds ?? 60) * 1000);
  const max = $derived(Math.max(1, ...buckets.map((b) => b.count ?? 0)));
  const multiDay = $derived(Date.parse(to) - Date.parse(from) > 86_400_000);
  const withSec = $derived((hist?.bucket_seconds ?? 60) < 60);
  const sizeText = $derived.by(() => {
    const s = hist?.bucket_seconds ?? 0;
    if (s >= 86400) return `${s / 86400} d`;
    if (s >= 3600) return `${s / 3600} h`;
    if (s >= 60) return `${s / 60} min`;
    return `${s} s`;
  });

  function tip(i: number): string {
    const b = buckets[i];
    const a = Math.max(Date.parse(b.start), Date.parse(from));
    const e = Math.min(Date.parse(b.start) + size, Date.parse(to));
    const when = `${label(a, multiDay, withSec)}–${label(e, multiDay, withSec)}${utc ? ' UTC' : ''}`;
    if (b.count === null) return `${when}: not counted yet`;
    return `${when}: ${b.approx ? '≈ ' : ''}${num(b.count)} line${b.count === 1 ? '' : 's'}${b.approx ? ' (estimated from the index)' : ''}`;
  }

  const ticks = $derived.by(() => {
    const n = buckets.length;
    if (!n) return [];
    const k = Math.min(5, n);
    const out: { left: number; text: string }[] = [];
    for (let j = 0; j < k; j++) {
      const i = Math.round((j * (n - 1)) / Math.max(1, k - 1));
      out.push({ left: ((i + 0.5) / n) * 100, text: label(Math.max(Date.parse(buckets[i].start), Date.parse(from)), multiDay, withSec) });
    }
    return out;
  });

  // ----- drag to pick a range -----
  let box: HTMLDivElement | undefined = $state();
  let drag = $state<{ a: number; b: number } | null>(null);

  function indexAt(e: PointerEvent): number {
    const r = box!.getBoundingClientRect();
    const x = Math.min(Math.max(e.clientX - r.left, 0), r.width - 1);
    return Math.floor((x / r.width) * buckets.length);
  }
  function down(e: PointerEvent) {
    if (!buckets.length || e.button !== 0) return;
    try {
      box!.setPointerCapture(e.pointerId);
    } catch {
      /* a synthetic event (tests) has no active pointer */
    }
    const i = indexAt(e);
    drag = { a: i, b: i };
  }
  function move(e: PointerEvent) {
    if (drag) drag = { a: drag.a, b: indexAt(e) };
  }
  function up() {
    if (!drag || !hist) return;
    const { a, b } = drag;
    drag = null;
    const r = snapSelection(buckets.map((x) => x.start), hist.bucket_seconds, a, b, from, to);
    if (Date.parse(r.from) < Date.parse(r.to) && (r.from !== from || r.to !== to)) onselect(r.from, r.to);
  }
  function key(e: KeyboardEvent, i: number) {
    if ((e.key === 'Enter' || e.key === ' ') && hist) {
      e.preventDefault();
      const r = snapSelection(buckets.map((x) => x.start), hist.bucket_seconds, i, i, from, to);
      onselect(r.from, r.to);
    }
  }
  const sel = $derived(drag ? { lo: Math.min(drag.a, drag.b), hi: Math.max(drag.a, drag.b) } : null);
</script>

<div class="histwrap" aria-busy={busy}>
  <div class="head">
    <span>Matches over time{hist ? ` · ${sizeText} buckets` : ''}</span>
    {#if hist?.source === 'index'}<span class="src" title="Counted from the index (per-minute and hourly stats, and file checkpoints), without reading the log files">from the index</span>{/if}
    {#if hist && buckets.some((b) => b.count === null)}<span class="legend"><span class="sw hatch" aria-hidden="true"></span>not counted yet</span>{/if}
    <span class="grow"></span>
    {#if hist}<span class="hint">Drag across the bars to zoom in; Back zooms out.</span>{/if}
  </div>
  <div
    class="hist"
    class:empty={!hist}
    bind:this={box}
    role="presentation"
    onpointerdown={down}
    onpointermove={move}
    onpointerup={up}
    onpointercancel={() => (drag = null)}
  >
    {#each buckets as b, i (b.start)}
      <button
        type="button"
        class="bar"
        class:pending={b.count === null}
        class:sel={sel && i >= sel.lo && i <= sel.hi}
        tabindex={i === 0 ? 0 : -1}
        title={tip(i)}
        aria-label={tip(i)}
        onkeydown={(e) => key(e, i)}
      >
        {#if b.count === null}<span class="fill hatch" style:height="100%"></span>
        {:else if b.count > 0}<span class="fill" style:height="{Math.max(3, (b.count / max) * 100)}%"></span>{/if}
      </button>
    {/each}
  </div>
  <div class="ticks" aria-hidden="true">
    {#each ticks as t, j (j)}<span style:left="{t.left}%">{t.text}</span>{/each}
  </div>
</div>

<style>
  .histwrap {
    padding: 8px 18px 4px;
    border-bottom: 1px solid rgba(255, 255, 255, 0.1);
    font-family: var(--font-sans);
    font-size: 12.5px;
    color: var(--log-dim);
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px;
    margin-bottom: 6px;
  }
  .grow {
    flex-grow: 1;
  }
  .src {
    color: var(--log-fg);
  }
  .legend {
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }
  .sw {
    display: inline-block;
    width: 14px;
    height: 10px;
    border-radius: 2px;
  }
  .hist {
    position: relative;
    display: flex;
    align-items: stretch;
    gap: 1px;
    height: 76px;
    cursor: crosshair;
    touch-action: none;
    user-select: none;
  }
  .hist.empty {
    background: rgba(255, 255, 255, 0.04);
    border-radius: 6px;
  }
  .bar {
    position: relative;
    flex: 1 1 0;
    min-width: 0;
    padding: 0;
    border: 0;
    background: transparent;
    cursor: crosshair;
    border-radius: 2px 2px 0 0;
  }
  .bar:hover,
  .bar:focus-visible {
    background: rgba(255, 255, 255, 0.08);
  }
  .bar.sel {
    background: rgba(157, 188, 255, 0.22);
  }
  .fill {
    position: absolute;
    left: 0;
    right: 0;
    bottom: 0;
    background: var(--log-host);
    border-radius: 2px 2px 0 0;
  }
  .hatch {
    background: repeating-linear-gradient(135deg, rgba(255, 255, 255, 0.16) 0 3px, transparent 3px 7px);
  }
  .ticks {
    position: relative;
    height: 18px;
    margin-top: 3px;
    font-family: var(--font-mono);
    font-size: 11.5px;
  }
  .ticks span {
    position: absolute;
    transform: translateX(-50%);
    white-space: nowrap;
  }
  .ticks span:first-child {
    transform: none;
    left: 0 !important;
  }
  .ticks span:last-child {
    transform: translateX(-100%);
    left: 100% !important;
  }
  .hint {
    font-size: 12px;
  }
</style>
