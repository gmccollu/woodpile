<script lang="ts">
  // Quick ranges, or a custom From → To on its own row under them. The custom row is one
  // line where there's room and stacks From and To (labels on the left) where there isn't,
  // decided by the component's own width (a container query), never by a mid-row wrap.
  import { PRESETS, fromInput, toInput, type Range } from '../lib/range';
  import { prefs } from '../lib/prefs.svelte';

  let { value = $bindable(), error = '' }: { value: Range; error?: string } = $props();

  const isCustom = $derived(value.preset === 'custom');

  function custom() {
    if (value.preset === 'custom') return;
    // Start the custom range from what the quick pick meant.
    const ms = PRESETS.find((p) => p.p === value.preset)?.ms ?? 3_600_000;
    const now = Date.now();
    value = { preset: 'custom', from: new Date(now - ms).toISOString(), to: new Date(now).toISOString() };
  }

  // Separate date and time fields: datetime-local renders very differently across browsers.
  const parts = (iso: string) => {
    const v = toInput(iso, prefs.utc);
    return { date: v.slice(0, 10), time: v.slice(11, 19) };
  };
  const fromParts = $derived(parts(value.from));
  const toParts = $derived(parts(value.to));

  function set(which: 'from' | 'to', field: 'date' | 'time', v: string) {
    const cur = which === 'from' ? fromParts : toParts;
    const next = { ...cur, [field]: v };
    if (!next.date) return;
    const iso = fromInput(`${next.date}T${next.time || '00:00:00'}`, prefs.utc);
    if (iso) value = { ...value, [which]: iso };
  }

  const span = $derived(isCustom && value.from && value.to ? Date.parse(value.to) - Date.parse(value.from) : NaN);
  const inlineError = $derived(isCustom && Number.isFinite(span) && span <= 0 ? '"From" must be before "To".' : '');
  const shownError = $derived(inlineError || error);

  /** "covers 24 h", "covers 3 d 4 h", "covers 45 min". */
  function covers(ms: number): string {
    const m = Math.round(ms / 60_000);
    if (m < 60) return `covers ${m} min`;
    const h = Math.floor(m / 60);
    if (h < 48) return `covers ${h} h${m % 60 ? ` ${m % 60} min` : ''}`;
    return `covers ${Math.floor(h / 24)} d${h % 24 ? ` ${h % 24} h` : ''}`;
  }
</script>

{#snippet zone()}
  <div class="seg" role="radiogroup" aria-label="Show times in">
    <button type="button" role="radio" aria-checked={!prefs.utc} onclick={() => (prefs.utc = false)}>Local</button>
    <button type="button" role="radio" aria-checked={prefs.utc} onclick={() => (prefs.utc = true)}>UTC</button>
  </div>
{/snippet}

<div class="tr" class:open={isCustom} role="group" aria-label="Time range">
  <div class="row1">
    <div class="seg" role="radiogroup" aria-label="Quick range">
      {#each PRESETS as p (p.p)}
        <button type="button" role="radio" aria-checked={value.preset === p.p} onclick={() => (value = { preset: p.p, from: '', to: '' })}>
          {p.label}
        </button>
      {/each}
      <button type="button" role="radio" aria-checked={isCustom} onclick={custom}>Custom</button>
    </div>
    {#if !isCustom}{@render zone()}{/if}
  </div>

  {#if isCustom}
    <div class="custom">
      <div class="pair from">
        <span class="lab" id="tr-from">From</span>
        <input class="in" type="date" aria-labelledby="tr-from" value={fromParts.date} onchange={(e) => set('from', 'date', e.currentTarget.value)} aria-invalid={!!shownError} />
        <input class="in" type="time" step="1" aria-label="From time" value={fromParts.time} onchange={(e) => set('from', 'time', e.currentTarget.value)} aria-invalid={!!shownError} />
      </div>
      <span class="arrow" aria-hidden="true">→</span>
      <div class="pair to">
        <span class="lab" id="tr-to">To</span>
        <input class="in" type="date" aria-labelledby="tr-to" value={toParts.date} onchange={(e) => set('to', 'date', e.currentTarget.value)} aria-invalid={!!shownError} />
        <input class="in" type="time" step="1" aria-label="To time" value={toParts.time} onchange={(e) => set('to', 'time', e.currentTarget.value)} aria-invalid={!!shownError} />
      </div>
      <div class="tail">
        {@render zone()}
        <span class="readout" aria-live="polite">{Number.isFinite(span) && span > 0 ? covers(span) : ''}</span>
      </div>
    </div>
  {/if}
  {#if shownError}<p class="ferr" role="alert">{shownError}</p>{/if}
</div>

<style>
  .tr {
    container-type: inline-size;
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: 0;
  }
  /* In a wrapping toolbar, the open custom row takes a line of its own. */
  .tr.open {
    flex-basis: 100%;
  }
  .row1 {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 12px;
  }
  .seg {
    display: inline-flex;
    background: var(--glass);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 3px;
  }
  .seg button {
    height: 28px;
    border: 0;
    background: transparent;
    color: var(--fg);
    padding: 0 12px;
    border-radius: 999px;
    font-size: 13px;
    cursor: pointer;
    white-space: nowrap;
  }
  .seg button[aria-checked='true'] {
    background: var(--chip);
    font-weight: 600;
    box-shadow: var(--shadow-chip);
  }

  /* Wide: one line, From → To · Local|UTC · readout. Controls match the chips (36 px). */
  .custom {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: nowrap;
  }
  .pair {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .lab {
    font-size: 13px;
    font-weight: 600;
  }
  .arrow {
    color: var(--muted);
  }
  .tail {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .readout {
    font-size: 13px;
    color: var(--muted);
    white-space: nowrap;
  }
  .in {
    height: 36px;
    padding: 0 10px;
    border-radius: 12px;
    border: 1px solid var(--input-border);
    background: var(--input);
    color: var(--fg);
    font: inherit;
    font-size: 13.5px;
    font-variant-numeric: tabular-nums;
  }
  .in[aria-invalid='true'] {
    border-color: var(--bad);
  }
  :global(.dark) .in {
    color-scheme: dark;
  }

  /* Narrow: From and To on their own rows, labels on the left; then the zone and readout. */
  @container (max-width: 760px) {
    .custom {
      display: grid;
      grid-template-columns: 3.2em minmax(0, 1fr) minmax(0, 1fr);
      align-items: center;
      gap: 8px;
    }
    .pair {
      display: contents;
    }
    .arrow {
      display: none;
    }
    .in {
      width: 100%;
      min-width: 0;
    }
    .tail {
      grid-column: 2 / -1;
      flex-wrap: wrap;
    }
  }

  .ferr {
    margin: 0;
    font-size: 13px;
    font-weight: 500;
    color: var(--bad-fg);
  }
</style>
