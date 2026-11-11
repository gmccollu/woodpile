<script lang="ts">
  // Toggle chips for a set of severities (docs/api.md → Severity filters). Alt-click a chip
  // for "only this one". Always on a solid dark strip, so the severity colours keep contrast.
  import { ALL_SEVS, type SevKey } from '../lib/api';
  import { SEVERITIES, sevClass } from '../lib/severity';

  let { value = $bindable(), label = 'Severities' }: { value: SevKey[]; label?: string } = $props();

  const name = (k: SevKey) => (k === 'unknown' ? 'unknown' : SEVERITIES[k].short);
  const full = (k: SevKey) => (k === 'unknown' ? 'Unknown (no PRI)' : SEVERITIES[k].name);
  const canon = (set: SevKey[]) => ALL_SEVS.filter((k) => set.includes(k));

  function toggle(k: SevKey, e: MouseEvent) {
    if (e.altKey) value = [k];
    else value = canon(value.includes(k) ? value.filter((x) => x !== k) : [...value, k]);
  }
</script>

<div class="sevchips" role="group" aria-label={label}>
  {#each ALL_SEVS as k (k)}
    <button
      type="button"
      class="chip {sevClass(k === 'unknown' ? null : k)}"
      aria-pressed={value.includes(k)}
      title="{full(k)}. Alt-click: only {name(k)}."
      onclick={(e) => toggle(k, e)}>{name(k)}</button
    >
  {/each}
  <span class="sep" aria-hidden="true"></span>
  <button type="button" class="mini" onclick={() => (value = [...ALL_SEVS])} disabled={value.length === ALL_SEVS.length}>All</button>
  <button type="button" class="mini" onclick={() => (value = [])} disabled={!value.length}>None</button>
</div>

<style>
  .sevchips {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px;
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .chip {
    padding: 2px 8px;
    border-radius: 999px;
    border: 1px solid rgba(255, 255, 255, 0.16);
    background: transparent;
    cursor: pointer;
    font: inherit;
    font-weight: 500;
  }
  .chip[aria-pressed='true'] {
    border-color: currentColor;
    background: rgba(255, 255, 255, 0.09);
  }
  .chip[aria-pressed='false'] {
    color: var(--log-dim);
    font-weight: 400;
    text-decoration: line-through;
    text-decoration-color: rgba(255, 255, 255, 0.3);
  }
  .sep {
    width: 1px;
    height: 16px;
    margin: 0 4px;
    background: rgba(255, 255, 255, 0.16);
  }
  .mini {
    padding: 2px 8px;
    border-radius: 999px;
    border: 1px solid rgba(255, 255, 255, 0.16);
    background: transparent;
    color: var(--log-fg);
    cursor: pointer;
    font-family: var(--font-sans);
    font-size: 12px;
  }
  .mini:disabled {
    opacity: 0.45;
    cursor: default;
  }
</style>
