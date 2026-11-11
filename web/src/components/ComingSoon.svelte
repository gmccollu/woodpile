<script lang="ts">
  let { title, intro, points }: { title: string; intro: string; points: string[] } = $props();
</script>

<section class="card max-w-3xl">
  <div class="flex items-center gap-3">
    <h1 class="m-0 text-2xl font-bold tracking-tight">{title}</h1>
    <span class="rounded-full bg-chip px-3 py-1 text-xs font-semibold text-accent-fg shadow-[var(--shadow-chip)]">Coming in M3</span>
  </div>
  <p class="mt-3 text-[15px] leading-relaxed">{intro}</p>
  <ul class="mt-2 list-disc space-y-1.5 pl-5 text-[15px] leading-relaxed">
    {#each points as p (p)}<li>{p}</li>{/each}
  </ul>
  <p class="mt-4 text-sm text-muted">
    This page is a placeholder: it isn't built yet. Until then, the logs are plain files in the data folder (see
    <a class="text-link underline" href="#/about">About</a>) and any text tool can read them.
  </p>
</section>
