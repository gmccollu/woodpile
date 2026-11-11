<script lang="ts">
  import type { DeviceStatus } from '../lib/api';
  let { status, label = false }: { status: DeviceStatus; label?: boolean } = $props();
  // Blocked is a choice, not a fault: a neutral ring, not the red offline dot.
  const color = { online: 'var(--ok)', quiet: 'var(--warn)', offline: 'var(--bad)', blocked: 'transparent' };
  const text = { online: 'Online', quiet: 'Quiet', offline: 'Offline', blocked: 'Blocked' };
</script>

<span class="inline-flex items-center gap-1.5">
  <span
    class="inline-block size-[9px] rounded-full"
    class:blocked={status === 'blocked'}
    style:background={color[status]}
    role={label ? undefined : 'img'}
    aria-label={label ? undefined : text[status]}
    title={text[status]}
  ></span>
  {#if label}<span>{text[status]}</span>{/if}
</span>

<style>
  .blocked {
    border: 2px solid var(--muted);
  }
</style>
