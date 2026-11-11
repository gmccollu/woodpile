<script lang="ts">
  import { mock, restartFirstRun, sendFirstMessage } from './server.svelte';
  import { refreshDevices, refreshStatus } from '../status.svelte';
  import { go } from '../router.svelte';

  let open = $state(false);

  function toggle(key: 'clash' | 'paused' | 'cycle') {
    mock[key] = !mock[key];
    refreshStatus();
  }
  function firstRun() {
    restartFirstRun();
    refreshDevices();
    go('welcome');
  }
  function testMessage() {
    sendFirstMessage();
    refreshDevices();
  }
</script>

{#if mock.panel}
  <div class="mock-panel" role="region" aria-label="Mock controls">
    <button class="mock-toggle" onclick={() => (open = !open)} aria-expanded={open}>
      Mock data {open ? '▾' : '▸'}
    </button>
    {#if open}
      <div class="mock-body">
        <label class="row">
          <span>Rate (msg/s)</span>
          <input type="number" min="0" step="1" bind:value={mock.rate} class="field" />
        </label>
        <div class="row presets">
          {#each [5, 40, 1284, 10000] as r (r)}
            <button class="ghost-btn" onclick={() => (mock.rate = r)}>{r.toLocaleString()}</button>
          {/each}
        </div>
        <label class="row"><input type="checkbox" checked={mock.clash} onchange={() => toggle('clash')} /> TCP 514 port clash</label>
        <label class="row"><input type="checkbox" checked={mock.paused} onchange={() => toggle('paused')} /> Writes paused (low disk)</label>
        <label class="row"><input type="checkbox" checked={mock.cycle} onchange={() => toggle('cycle')} /> Cycle branch-ap-08 quiet/offline</label>
        <label class="row">
          <span>Latency (ms)</span>
          <input type="number" min="0" step="10" bind:value={mock.latency} class="field" />
        </label>
        <div class="row presets">
          <button class="ghost-btn" onclick={firstRun}>Restart as first run</button>
          <button class="ghost-btn" onclick={testMessage} disabled={!mock.firstRun}>Send test message</button>
        </div>
        <p class="hint">Settings validation: try port 70000, a bad CIDR, or offline &lt; quiet.</p>
      </div>
    {/if}
  </div>
{/if}

<style>
  .mock-panel {
    position: fixed;
    right: 16px;
    bottom: 16px;
    z-index: 50;
    width: 260px;
    font-size: 13px;
    color: var(--fg);
    background: var(--glass-strong);
    border: 1px dashed var(--muted);
    border-radius: 16px;
    padding: 8px;
    backdrop-filter: blur(16px);
    box-shadow: var(--shadow);
  }
  .mock-toggle {
    width: 100%;
    text-align: left;
    background: none;
    border: 0;
    color: var(--fg);
    font-weight: 600;
    cursor: pointer;
    padding: 4px 6px;
  }
  .mock-body {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 6px;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .row span {
    flex: 1;
  }
  .row .field {
    width: 100px;
    padding: 4px 8px;
  }
  .presets {
    flex-wrap: wrap;
    gap: 6px;
  }
  .presets :global(.ghost-btn) {
    padding: 3px 9px;
    font-size: 12px;
  }
  .hint {
    margin: 0;
    color: var(--muted);
    font-size: 12px;
  }
</style>
