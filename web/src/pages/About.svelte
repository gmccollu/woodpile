<script lang="ts">
  import { bytes, dateTime, duration } from '../lib/format';
  import { live } from '../lib/status.svelte';
  import CopyButton from '../components/CopyButton.svelte';

  const st = $derived(live.status);
  const osName = $derived(
    st?.os.startsWith('darwin') ? 'macOS' : st?.os.startsWith('windows') ? 'Windows' : st?.os.startsWith('linux') ? 'Linux' : (st?.os ?? ''),
  );
  const shortBuf = $derived(!!st && st.udp_rcvbuf_bytes < st.udp_rcvbuf_requested_bytes);
  const bufHint = $derived(
    st?.os.startsWith('linux')
      ? 'The kernel caps it at net.core.rmem_max. The deb and rpm packages raise that to 32 MiB; if you installed from the tarball, set it yourself in /etc/sysctl.d/.'
      : st?.os.startsWith('darwin')
        ? 'macOS caps socket buffers with the kern.ipc.maxsockbuf sysctl.'
        : 'The OS gave a smaller buffer than requested.',
  );
</script>

<div class="mx-auto max-w-3xl space-y-4">
  <section class="card">
    <h1 class="m-0 text-xl font-bold tracking-tight">About Woodpile</h1>
    {#if !st}
      <p class="text-muted">Loading…</p>
    {:else}
      <dl class="mt-4 grid grid-cols-[max-content_1fr] gap-x-6 gap-y-3 text-[14.5px]">
        <dt>Version</dt>
        <dd><b>{st.version}</b> <span class="text-muted">(build {st.build})</span></dd>
        <dt>Running on</dt>
        <dd>{osName} <span class="text-muted">({st.os})</span></dd>
        <dt>Started</dt>
        <dd>{dateTime(st.started_at)} <span class="text-muted">· up {duration(st.started_at, live.now)}</span></dd>
        <dt>Data folder</dt>
        <dd class="flex flex-wrap items-center gap-2">
          <code class="font-mono text-[13.5px] break-all">{st.data_dir}</code>
          <CopyButton text={st.data_dir} />
        </dd>
        <dt>UDP receive buffer</dt>
        <dd>
          <b>{bytes(st.udp_rcvbuf_bytes)}</b>
          <span class="text-muted">(asked for {bytes(st.udp_rcvbuf_requested_bytes)})</span>
          {#if shortBuf}
            <p class="mt-1 mb-0 text-[13px] text-warn-fg">Smaller than requested, so a burst is more likely to overflow it. {bufHint}</p>
          {/if}
        </dd>
        <dt>Releases</dt>
        <dd>
          {#if st.releases_url}
            <a class="text-link underline break-all" href={st.releases_url} rel="noreferrer noopener" target="_blank">{st.releases_url}</a>
          {:else}
            <span class="text-muted">No public releases page yet.</span>
          {/if}
          <p class="mt-1 mb-0 text-[13px] text-muted">Woodpile never checks for updates or connects out; compare this version with the releases page yourself.</p>
        </dd>
      </dl>
    {/if}
  </section>

  {#if st}
    <section class="card">
      <h2 class="m-0 mb-2 text-[17px] font-bold">Dropped messages</h2>
      <p class="m-0 text-[14.5px] leading-relaxed">
        Woodpile counts every message it drops itself (when the low-disk guard pauses writing, or a write fails), and shows the count on the
        dashboard.
      </p>
      {#if st.kernel_drops_visible}
        <p class="mt-2 mb-0 text-[14.5px] leading-relaxed">
          On {osName} it also reads the kernel's UDP drop counter for its socket, so packets the OS discarded before Woodpile could read them
          (for example when the receive buffer overflows) are counted too.
        </p>
      {:else}
        <p class="mt-2 mb-0 rounded-xl border border-[var(--warn-border)] bg-[var(--warn-bg)] px-3 py-2 text-[14.5px] leading-relaxed">
          <b>{osName} doesn't tell programs how many UDP packets the kernel dropped.</b> If the receive buffer overflows during a burst, those
          messages are lost before Woodpile sees them, and no counter here can show it. A dropped count of 0 means Woodpile itself lost
          nothing; it can't prove the kernel didn't. Use TCP for senders where every message matters.
        </p>
      {/if}
    </section>

    <section class="card">
      <h2 class="m-0 mb-2 text-[17px] font-bold">Listeners</h2>
      <ul class="m-0 list-none space-y-1.5 p-0 text-[14.5px]">
        {#each st.listeners as l (l.proto + l.port)}
          <li class="flex items-center gap-2">
            <span class="inline-block size-[9px] rounded-full" style:background={l.ok ? 'var(--ok)' : 'var(--bad)'} aria-hidden="true"></span>
            <span class="font-mono text-[13.5px]">{l.proto.toUpperCase()} {l.addr}:{l.port}</span>
            <span class:text-ok-fg={l.ok} class:text-bad-fg={!l.ok}>{l.ok ? 'listening' : l.error}</span>
          </li>
        {/each}
      </ul>
      <p class="mt-3 mb-0 text-[13px] text-muted">
        Host addresses: {st.host_ips.join(', ')} · <a class="text-link underline" href="#/welcome">Setup guide</a>
      </p>
    </section>
  {/if}
</div>

<style>
  dt {
    color: var(--muted);
    font-weight: 500;
  }
  dd {
    margin: 0;
  }
</style>
