<script lang="ts">
  import { num } from '../lib/format';
  let { values, height = 34 }: { values: number[]; height?: number } = $props();

  const W = 200;
  const pts = $derived.by(() => {
    if (values.length < 2) return '';
    const max = Math.max(...values, 1);
    const min = Math.min(...values, 0);
    const span = max - min || 1;
    return values
      .map((v, i) => `${((i / (values.length - 1)) * W).toFixed(1)},${(height - 3 - ((v - min) / span) * (height - 6)).toFixed(1)}`)
      .join(' ');
  });
  const summary = $derived(
    values.length ? `Last hour: low ${num(Math.min(...values))}, high ${num(Math.max(...values))} msg/s` : 'No data yet',
  );
</script>

<svg width="100%" {height} viewBox="0 0 {W} {height}" preserveAspectRatio="none" role="img" aria-label={summary}>
  <title>{summary}</title>
  {#if pts}
    <polyline points="0,{height} {pts} {W},{height}" fill="var(--spark)" opacity="0.12" stroke="none" />
    <polyline
      points={pts}
      fill="none"
      stroke="var(--spark)"
      stroke-width="2.2"
      stroke-linecap="round"
      stroke-linejoin="round"
      vector-effect="non-scaling-stroke"
    />
  {/if}
</svg>
