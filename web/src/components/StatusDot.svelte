<script lang="ts">
  import type { DeviceStatus } from '../lib/api';
  let { status, label = false }: { status: DeviceStatus; label?: boolean } = $props();
  const color = { online: 'var(--ok)', quiet: 'var(--warn)', offline: 'var(--bad)' };
  const text = { online: 'Online', quiet: 'Quiet', offline: 'Offline' };
</script>

<span class="inline-flex items-center gap-1.5">
  <span
    class="inline-block size-[9px] rounded-full"
    style:background={color[status]}
    role={label ? undefined : 'img'}
    aria-label={label ? undefined : text[status]}
    title={text[status]}
  ></span>
  {#if label}<span>{text[status]}</span>{/if}
</span>
