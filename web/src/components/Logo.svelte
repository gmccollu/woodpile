<script lang="ts">
  let { size = 30 }: { size?: number } = $props();
  const logs = [
    [8, 21],
    [22, 21],
    [15, 9],
  ];
</script>

<svg width={size} height={size} viewBox="0 0 30 30" aria-hidden="true">
  {#each logs as [x, y] (`${x},${y}`)}
    <circle cx={x} cy={y} r="6.5" fill="#e3b77a" stroke="#9a6a34" stroke-width="1.6" />
    <circle cx={x} cy={y} r="3.4" fill="none" stroke="#9a6a34" stroke-width="1.1" />
    <circle cx={x} cy={y} r="1" fill="#9a6a34" />
  {/each}
</svg>
