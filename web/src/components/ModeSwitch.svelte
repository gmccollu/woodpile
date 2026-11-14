<script lang="ts">
  // Text | Terms | Regex (SPEC §10.1.3): how the text box (and Exclude) is read.
  import type { Mode } from '../lib/events';

  let { value = $bindable() }: { value: Mode } = $props();

  const MODES: { m: Mode; label: string; title: string }[] = [
    { m: 'text', label: 'Text', title: 'One piece of text, matched anywhere in the message (the default)' },
    { m: 'terms', label: 'Terms', title: 'Words and "quoted phrases" joined by AND, OR and NOT (upper case), with ( ) to group' },
    { m: 'regex', label: 'Regex', title: 'A regular expression (RE2 syntax)' },
  ];
</script>

<div class="modes" role="group" aria-label="How the text is read">
  {#each MODES as x (x.m)}
    <button type="button" aria-pressed={value === x.m} title={x.title} onclick={() => (value = x.m)}>{x.label}</button>
  {/each}
</div>

<style>
  .modes {
    display: inline-flex;
    border: 1px solid var(--input-border);
    border-radius: 12px;
    background: var(--input);
    overflow: hidden;
  }
  .modes button {
    padding: 8px 11px;
    border: 0;
    background: transparent;
    color: var(--fg);
    font-size: 13px;
    font-family: var(--font-mono);
    cursor: pointer;
    white-space: nowrap;
  }
  .modes button + button {
    border-left: 1px solid var(--input-border);
  }
  .modes button[aria-pressed='true'] {
    background: var(--btn);
    color: var(--btn-fg);
  }
</style>
