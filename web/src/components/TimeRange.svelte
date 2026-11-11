<script lang="ts">
  import { PRESETS, fromInput, toInput, type Range } from '../lib/range';
  import { prefs } from '../lib/prefs.svelte';

  let { value = $bindable(), error = '' }: { value: Range; error?: string } = $props();

  function custom() {
    if (value.preset === 'custom') return;
    // Start the custom range from what the quick pick meant.
    const ms = PRESETS.find((p) => p.p === value.preset)?.ms ?? 3_600_000;
    const now = Date.now();
    value = { preset: 'custom', from: new Date(now - ms).toISOString(), to: new Date(now).toISOString() };
  }
</script>

<div class="tr" role="group" aria-label="Time range">
  <div class="seg" role="radiogroup" aria-label="Quick range">
    {#each PRESETS as p (p.p)}
      <button type="button" role="radio" aria-checked={value.preset === p.p} onclick={() => (value = { preset: p.p, from: '', to: '' })}>
        {p.label}
      </button>
    {/each}
    <button type="button" role="radio" aria-checked={value.preset === 'custom'} onclick={custom}>Custom</button>
  </div>
  {#if value.preset === 'custom'}
    <label class="dt">
      <span>From</span>
      <input
        class="field"
        type="datetime-local"
        step="1"
        value={toInput(value.from, prefs.utc)}
        onchange={(e) => (value = { ...value, from: fromInput(e.currentTarget.value, prefs.utc) })}
        aria-invalid={!!error}
      />
    </label>
    <label class="dt">
      <span>To</span>
      <input
        class="field"
        type="datetime-local"
        step="1"
        value={toInput(value.to, prefs.utc)}
        onchange={(e) => (value = { ...value, to: fromInput(e.currentTarget.value, prefs.utc) })}
        aria-invalid={!!error}
      />
    </label>
  {/if}
  <div class="seg" role="radiogroup" aria-label="Show times in">
    <button type="button" role="radio" aria-checked={!prefs.utc} onclick={() => (prefs.utc = false)}>Local time</button>
    <button type="button" role="radio" aria-checked={prefs.utc} onclick={() => (prefs.utc = true)}>UTC</button>
  </div>
  {#if error}<p class="ferr">{error}</p>{/if}
</div>

<style>
  .tr {
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
  .dt {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 13px;
  }
  .dt .field {
    width: auto;
    padding: 5px 10px;
    font-size: 13px;
  }
  .ferr {
    flex-basis: 100%;
    margin: 0;
    font-size: 13px;
    font-weight: 500;
    color: var(--bad-fg);
  }
</style>
