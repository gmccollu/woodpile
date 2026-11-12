<script lang="ts">
  // The woodshed mark (the same drawing as web/public/favicon.svg): a lean-to roof on two
  // posts, with three log ends stacked under it. The posts and floor sit on the 16 px grid.
  let { size = 30 }: { size?: number } = $props();
  const logs = [
    [10.6, 21.1],
    [19.4, 21.1],
    [15, 13.6],
  ];
</script>

<svg width={size} height={size} viewBox="0 0 30 30" aria-hidden="true">
  <g stroke="#9a6a34" stroke-linecap="round" stroke-linejoin="round" fill="none">
    <path d="M1.9 3.4 28.1 8.4" stroke-width="3.4" />
    <path d="M2.8 4.8v22.4M27.2 9.4v17.8M2.8 27.2h24.4" stroke-width="2" />
  </g>
  {#each logs as [x, y] (`${x},${y}`)}
    <circle cx={x} cy={y} r="4.3" fill="#e3b77a" stroke="#9a6a34" stroke-width="1.6" />
    <circle cx={x} cy={y} r="1.45" fill="#9a6a34" />
  {/each}
</svg>
