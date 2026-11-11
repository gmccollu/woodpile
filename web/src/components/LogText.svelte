<script lang="ts">
  // A message as one visual row: line breaks, control and format characters become
  // markers (or escapes in raw view), with search matches highlighted. Rows longer than
  // ROW_CAP characters render only the start, with an expander for the rest.
  import { ROW_CAP, tokens } from '../lib/logtext';
  import { bytes } from '../lib/format';

  let { text, re = null, raw = false }: { text: string; re?: RegExp | null; raw?: boolean } = $props();
  let expanded = $state(false);

  const long = $derived(text.length > ROW_CAP);
  const toks = $derived(tokens(long && !expanded ? text.slice(0, ROW_CAP) : text, re, raw));
  const size = $derived(long ? bytes(new TextEncoder().encode(text).length) : '');

  function expand(e: MouseEvent) {
    e.stopPropagation(); // the row itself may be clickable (search results)
    expanded = true;
  }
</script>

{#each toks as k, i (i)}{#if k.kind === 'mark'}<span class="brk" class:hl={k.hit} aria-label={k.t === '↵' ? 'line break' : `invisible character ${k.t}`}
      >{k.t}</span
    >{:else if k.kind === 'esc'}<span class="esc" class:hl={k.hit}>{k.t}</span>{:else if k.hit}<mark class="hl">{k.t}</mark>{:else}{k.t}{/if}{/each}{#if long && !expanded}<span
    class="more">… </span
  ><button type="button" class="more-btn" onclick={expand} onkeydown={(e) => e.stopPropagation()}>show full message ({size})</button>{/if}

<style>
  .brk {
    color: var(--log-dim);
    user-select: none;
    -webkit-user-select: none;
  }
  .esc {
    color: var(--log-dim);
  }
  .hl {
    background: rgba(243, 192, 107, 0.3);
    border-radius: 2px;
  }
  .more {
    color: var(--log-dim);
  }
  .more-btn {
    font: inherit;
    font-family: var(--font-sans);
    font-size: 12px;
    color: var(--log-host);
    background: none;
    border: 1px solid rgba(255, 255, 255, 0.2);
    border-radius: 999px;
    padding: 0 8px;
    cursor: pointer;
    text-indent: 0;
  }
</style>
