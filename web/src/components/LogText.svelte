<script lang="ts">
  // A message as one visual row: line breaks and other control characters become
  // markers (or escapes in raw view), with search matches highlighted.
  import { tokens } from '../lib/logtext';

  let { text, re = null, raw = false }: { text: string; re?: RegExp | null; raw?: boolean } = $props();
  const toks = $derived(tokens(text, re, raw));
</script>

{#each toks as k, i (i)}{#if k.kind === 'mark'}<span class="brk" class:hl={k.hit} aria-label="line break">{k.t}</span>{:else if k.kind === 'esc'}<span
      class="esc"
      class:hl={k.hit}>{k.t}</span
    >{:else if k.hit}<mark class="hl">{k.t}</mark>{:else}{k.t}{/if}{/each}

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
</style>
