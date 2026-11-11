<script lang="ts">
  import { retryListeners } from '../lib/api';
  import { live, refreshStatus } from '../lib/status.svelte';
  import Icon from './Icon.svelte';

  let retrying = $state(false);
  let retryError = $state('');

  const failed = $derived(live.status?.listeners.filter((l) => !l.ok) ?? []);

  async function retry() {
    retrying = true;
    retryError = '';
    try {
      await retryListeners();
      await refreshStatus();
      if (live.status?.listeners.some((l) => !l.ok)) retryError = 'Still failing. Check the other program is stopped, then try again.';
    } catch (e) {
      retryError = (e as Error).message;
    } finally {
      retrying = false;
    }
  }

</script>

{#if live.statusError}
  <div class="banner error" role="alert">
    <Icon name="alert" />
    <div class="grow">Can't reach the Woodpile service ({live.statusError}). Retrying every 5 s.</div>
  </div>
{/if}
{#each live.status?.banners ?? [] as b, i (`${i}:${b.kind}`)}
  <div class="banner {b.level}" role={b.level === 'error' ? 'alert' : 'status'}>
    <Icon name="alert" />
    <div class="grow">
      <div>{b.text}</div>
      {#if b.kind === 'device_limit'}
        <div class="detail-text">
          The limits stop a flood of fake source addresses from filling the device list. If these are real devices, raise the limits in
          <a class="text-link underline" href="#/settings?section=limits">Settings → Sender limits</a>.
        </div>
      {/if}
      {#if b.kind === 'listener_bind'}
        {#each failed as l (l.proto + l.port)}
          <div class="detail">
            {l.proto.toUpperCase()} {l.addr}:{l.port}: {l.error}{l.holder ? ` (held by ${l.holder})` : ''}
          </div>
        {/each}
      {/if}
      {#if b.action === 'retry_listeners' && retryError}<div class="detail" role="status">{retryError}</div>{/if}
    </div>
    {#if b.action === 'retry_listeners'}
      <button class="pill-btn" onclick={retry} disabled={retrying}>
        <Icon name="retry" size={15} />{retrying ? 'Retrying…' : 'Retry'}
      </button>
    {/if}
  </div>
{/each}

<style>
  .banner {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 12px 16px;
    border-radius: 18px;
    margin-bottom: 12px;
    font-size: 14px;
    line-height: 1.45;
    color: var(--fg);
    border: 1px solid;
  }
  .banner :global(svg) {
    margin-top: 1px;
  }
  .error {
    background: var(--error-bg);
    border-color: var(--error-border);
  }
  .error > :global(svg) {
    color: var(--bad-fg);
  }
  .warn {
    background: var(--warn-bg);
    border-color: var(--warn-border);
  }
  .warn > :global(svg) {
    color: var(--warn-fg);
  }
  .info {
    background: var(--info-bg);
    border-color: var(--info-border);
  }
  .detail-text {
    margin-top: 4px;
    font-size: 13.5px;
  }
  .detail {
    margin-top: 4px;
    font-family: var(--font-mono);
    font-size: 12.5px;
  }
</style>
