<script lang="ts">
  // Settings → Syslog over TLS, the part that comes from GET /api/tls rather than the form:
  // the CA and certificate, downloads, Renew and Replace CA, the firewall hint and the
  // failed handshakes (docs/api.md → TLS syslog listener).
  import { ApiError, downloadCa, getTls, getTlsFailures, renewTls, type CaFormat, type TlsFailure, type TlsState } from '../lib/api';
  import { ago, dateTime, num } from '../lib/format';
  import { deviceName, live, refreshStatus } from '../lib/status.svelte';
  import { daysLeft, hostFirewallCommands, tlsReason, ymd } from '../lib/tls';
  import CopyButton from './CopyButton.svelte';
  import Icon from './Icon.svelte';

  /** The saved settings' listeners.tls.enabled and port; `reload` changes after every save. */
  let { enabled, port, reload = 0 }: { enabled: boolean; port: number; reload?: number } = $props();

  let tls = $state<TlsState | null>(null);
  let loadError = $state('');
  let failures = $state<TlsFailure[] | null>(null);

  async function load() {
    try {
      tls = await getTls();
      loadError = '';
    } catch (e) {
      loadError = (e as Error).message;
    }
    try {
      failures = await getTlsFailures();
    } catch {
      failures = failures ?? [];
    }
  }

  $effect(() => {
    void reload;
    load();
    const t = setInterval(load, 15000);
    return () => clearInterval(t);
  });

  const os = $derived(live.status?.os ?? '');
  const isWindows = $derived(os.startsWith('windows'));
  const isMac = $derived(os.startsWith('darwin'));
  const fw = $derived(hostFirewallCommands(port));
  const file = $derived(tls?.source === 'file');

  // ---- Downloads ----
  let dlError = $state('');
  let dlBusy = $state<CaFormat | null>(null);
  async function download(f: CaFormat) {
    dlBusy = f;
    dlError = '';
    try {
      await downloadCa(f);
    } catch (e) {
      dlError = `Couldn't download the CA certificate: ${(e as Error).message}`;
    } finally {
      dlBusy = null;
    }
  }

  // ---- Renew and Replace CA, each behind a confirmation ----
  let confirm = $state<'renew' | 'new_ca' | null>(null);
  let understood = $state(false);
  let busy = $state(false);
  let actionError = $state('');
  let done = $state('');
  let cancelBtn: HTMLButtonElement | undefined = $state();

  function ask(kind: 'renew' | 'new_ca') {
    confirm = kind;
    understood = false;
    actionError = '';
    done = '';
  }

  $effect(() => {
    if (!confirm) return;
    cancelBtn?.focus();
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !busy) confirm = null;
    };
    addEventListener('keydown', key);
    return () => removeEventListener('keydown', key);
  });

  async function renew() {
    if (!confirm || busy) return;
    const newCa = confirm === 'new_ca';
    busy = true;
    actionError = '';
    try {
      tls = await renewTls(newCa);
      confirm = null;
      done = newCa
        ? 'The CA was replaced. Download the new CA certificate and import it on every device that sends over TLS.'
        : 'The certificate was renewed. Devices need no change: they trust the same CA.';
      refreshStatus();
    } catch (e) {
      actionError = e instanceof ApiError && e.status === 409 ? e.message : `Couldn't ${newCa ? 'replace the CA' : 'renew the certificate'}: ${(e as Error).message}`;
    } finally {
      busy = false;
    }
  }

  /** "in 41 days" (warn within 60 days, as the banner does). */
  function expiry(iso: string): { text: string; soon: boolean } {
    const d = daysLeft(iso, live.now);
    if (d < 0) return { text: 'expired', soon: true };
    if (d < 60) return { text: d === 0 ? 'today' : `in ${d} day${d === 1 ? '' : 's'}`, soon: true };
    return { text: `in about ${Math.round(d / 365)} year${Math.round(d / 365) === 1 ? '' : 's'}`, soon: false };
  }

  const caEx = $derived(tls?.ca ? expiry(tls.ca.not_after) : null);
  const certEx = $derived(tls?.cert ? expiry(tls.cert.not_after) : null);

  const fwText = $derived<Record<string, string>>({
    missing: 'The Windows firewall has no rule for TLS syslog yet, so devices may not get through.',
    disabled: 'The Windows firewall rule for TLS syslog is turned off, so devices may not get through.',
    wrong_port: `The Windows firewall rule for TLS syslog is for a different port, not ${port}.`,
    unknown: `Woodshed can't tell whether the Windows firewall lets devices in on port ${port}.`,
  });
</script>

{#snippet fingerprint(label: string, value: string)}
  <div class="fp-row">
    <span class="fp-label">{label}</span>
    <code class="fp">{value}</code>
    <CopyButton text={value} label="Copy" />
  </div>
{/snippet}

{#if loadError && !tls}
  <p class="hint text-bad-fg" role="alert">Couldn't read the TLS state: {loadError}</p>
{:else if !tls}
  <p class="hint">Loading…</p>
{:else}
  {#if isWindows && enabled && tls.firewall.state !== 'ok' && tls.firewall.command}
    <div class="fwbox" role="note" data-tls-firewall>
      <p class="m-0 text-[13.5px]">
        <b>{fwText[tls.firewall.state] ?? 'The Windows firewall may block devices.'}</b> Run this once in a Command Prompt or PowerShell opened with
        <b>Run as administrator</b>:
      </p>
      <div class="log-panel mt-2 flex items-start gap-3 rounded-2xl p-3">
        <code class="grow text-[13px] leading-relaxed break-all whitespace-pre-wrap">{tls.firewall.command}</code>
        <CopyButton text={tls.firewall.command} />
      </div>
    </div>
  {/if}

  {#if done}<p class="done" role="status">{done}</p>{/if}

  {#if !tls.cert}
    <div class="empty" data-tls-none>
      <p class="m-0 text-[14px] font-semibold">No certificate yet.</p>
      <p class="hint mt-1 mb-0">
        Turn on Syslog over TLS and save: Woodshed then creates its own CA and a certificate signed by it. Devices import the CA certificate once, and a
        renewed certificate needs nothing on them.
      </p>
    </div>
  {:else}
    {#if !enabled}
      <p class="hint">TLS is off. The certificate below is kept, so devices that already trust the CA work again when you turn it back on.</p>
    {/if}
    <div class="certs" id="tls-cert">
      {#if tls.ca && !file}
        <div class="cert">
          <h3 class="ch">CA certificate <span class="sub">what devices import</span></h3>
          <dl>
            <dt>Subject</dt>
            <dd class="font-mono break-all">{tls.ca.subject}</dd>
            <dt>Expires</dt>
            <dd title={dateTime(tls.ca.not_after)}>{ymd(tls.ca.not_after)} <span class:soon={caEx?.soon} class="text-muted">({caEx?.text})</span></dd>
          </dl>
          {@render fingerprint('SHA-256', tls.ca.sha256)}
          {@render fingerprint('SHA-1', tls.ca.sha1)}
          <p class="hint mt-2 mb-2">
            Import it into each device's trust store as a trusted CA certificate. Before you trust it there, check that the fingerprint the device shows
            matches this one.
          </p>
          <div class="dl">
            <button type="button" class="ghost-btn" onclick={() => download('pem')} disabled={dlBusy !== null}>
              <Icon name="download" size={15} />{dlBusy === 'pem' ? 'Downloading…' : 'Download PEM'}
            </button>
            <span class="hint m-0">For devices where you paste the certificate as text.</span>
          </div>
          <div class="dl">
            <button type="button" class="ghost-btn" onclick={() => download('der')} disabled={dlBusy !== null}>
              <Icon name="download" size={15} />{dlBusy === 'der' ? 'Downloading…' : 'Download DER (.cer)'}
            </button>
            <span class="hint m-0">For devices that want a file to upload, and for the Windows certificate store.</span>
          </div>
          {#if dlError}<p class="ferr" role="alert">{dlError}</p>{/if}
        </div>
      {/if}

      <div class="cert">
        <h3 class="ch">{file ? 'Your certificate' : 'Server certificate'} <span class="sub">what Woodshed sends</span></h3>
        <dl>
          <dt>Subject</dt>
          <dd class="font-mono break-all">{tls.cert.subject}</dd>
          {#if file}
            <dt>Issuer</dt>
            <dd class="font-mono break-all">{tls.cert.issuer}</dd>
          {/if}
          <dt>Expires</dt>
          <dd title={dateTime(tls.cert.not_after)}>{ymd(tls.cert.not_after)} <span class:soon={certEx?.soon} class="text-muted">({certEx?.text})</span></dd>
        </dl>
        {@render fingerprint('SHA-256', tls.cert.sha256)}
        <p class="mt-3 mb-1 text-[13.5px] font-semibold">DNS names</p>
        <ul class="names" aria-label="DNS names in the certificate">
          {#each tls.cert.dns_names as name (name)}<li><code>{name}</code></li>{/each}
        </ul>
        <p class="hint mt-1 mb-0">
          If a device checks the server's name, give it one of these DNS names, not an IP address: many devices check DNS names only.
        </p>
        {#if tls.cert.ip_addresses.length}
          <p class="hint mt-1 mb-0">Also covers {tls.cert.ip_addresses.join(', ')}.</p>
        {/if}
        {#if tls.cert.missing_names.length}
          <p class="missing" role="note">
            Not covered: <b>{tls.cert.missing_names.join(', ')}</b>. Devices that check the server name will refuse that name.
            {#if !file}Renew the certificate to add it.{/if}
          </p>
        {/if}
        {#if file}
          <p class="hint mt-3 mb-0">
            Woodshed sends this certificate as it is in the file, and reads the files again on every save and on Retry. To change it, replace the files.
            There's no CA download: get the CA certificate from whoever issued it.
          </p>
        {/if}
      </div>
    </div>

    {#if !file && tls.ca}
      <div class="renew">
        <button type="button" class="ghost-btn" onclick={() => ask('renew')}><Icon name="retry" size={15} />Renew certificate…</button>
        <button type="button" class="ghost-btn" onclick={() => ask('new_ca')}>Replace CA…</button>
        <span class="hint m-0">Nothing renews on its own: a banner warns 60 days before either one expires.</span>
      </div>
    {/if}
  {/if}

  <details class="trouble" id="tls-help">
    <summary>If devices can't connect</summary>
    <ul>
      <li>
        The device must trust the CA: import {file ? "your certificate's CA" : "Woodshed's CA certificate"} into its trust store as a trusted CA, and check the
        fingerprint.
      </li>
      <li>If the device checks the server's name, give it one of the DNS names above, not an IP address.</li>
      <li>Point it at TCP port {port} with TLS. Plain syslog sent to this port shows as "Not TLS" below.</li>
      <li>A device that offers only TLS 1.0 or 1.1 needs <b>Allow TLS 1.0 and 1.1</b> above.</li>
      <li>
        Some firewalls block traffic through them while a TLS log host can't be reached or its certificate fails, unless an option permits traffic when
        the log host is down. Check that option before you turn on TLS logging on a firewall.
      </li>
    </ul>
    {#if isWindows}
      <p class="hint mb-2">
        Windows firewall: <code>woodshed tls on</code>, run once in an administrator prompt, turns the listener on and opens port {port} for Woodshed.
      </p>
    {:else if isMac}
      <p class="hint mb-2">
        The macOS firewall allows or blocks programs, not ports: if Woodshed already receives syslog on UDP or TCP, TLS gets through too.
      </p>
    {:else}
      <p class="hint mb-2">
        Host firewall: open TCP port {port}. <code>sudo woodshed tls on</code> does this for you when ufw or firewalld is active. Otherwise, with ufw:
      </p>
      <div class="cmd log-panel">
        <code>{fw.ufw}</code>
        <CopyButton text={fw.ufw} />
      </div>
      <p class="hint mt-2 mb-2">Or with firewalld:</p>
      <div class="cmd log-panel">
        <code>{fw.firewalld}</code>
        <CopyButton text={fw.firewalld} />
      </div>
    {/if}
  </details>

  <h3 class="mt-4 mb-1 text-[14px] font-semibold" id="tls-failures">Failed TLS handshakes, last 24 h</h3>
  <p class="hint mt-0">A sender that only fails never gets a message through, so it shows here and nowhere else.</p>
  {#if failures === null}
    <p class="hint">Loading…</p>
  {:else if !failures.length}
    <p class="hint">None.</p>
  {:else}
    <div class="max-h-80 overflow-auto rounded-xl border border-[var(--divider)]">
      <table class="w-full border-collapse text-[13px]">
        <thead>
          <tr>
            <th scope="col">Sender</th>
            <th scope="col" class="text-right">Failures</th>
            <th scope="col">Last seen</th>
            <th scope="col">Why</th>
            <th scope="col">Detail</th>
          </tr>
        </thead>
        <tbody>
          {#each failures as f (f.ip)}
            <tr>
              <td class="whitespace-nowrap">
                <span class="font-mono">{f.ip}</span>
                {#if f.device_id}
                  <a class="text-link ml-1 underline" href="#/devices?q={encodeURIComponent(f.ip)}" title="Open this device on the Devices page">
                    {deviceName(f.device_id) !== f.device_id ? deviceName(f.device_id) : 'device'}
                  </a>
                {/if}
              </td>
              <td class="text-right tabular-nums">{num(f.count_24h)}</td>
              <td class="whitespace-nowrap" title={dateTime(f.last_seen)}>{ago(f.last_seen, live.now)}</td>
              <td>{tlsReason(f.reason)}</td>
              <td class="detail font-mono text-[12px] text-muted">{f.detail}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
{/if}

{#if confirm}
  <div class="scrim" role="presentation" onclick={() => !busy && (confirm = null)}></div>
  <div class="dialog glass" role="alertdialog" aria-modal="true" aria-labelledby="tls-dlg-t" aria-describedby="tls-dlg-d">
    {#if confirm === 'renew'}
      <h2 id="tls-dlg-t" class="m-0 text-[18px] font-bold">Renew the TLS certificate?</h2>
      <div id="tls-dlg-d" class="mt-2 text-[14.5px]">
        <p class="m-0">
          Woodshed makes a new certificate with a new key and this computer's current names, signed by the same CA. Devices keep working: they trust the
          CA, which doesn't change.
        </p>
        <p class="mt-2 mb-0 text-[13.5px] text-muted">New connections get the new certificate; open ones keep the old one until they reconnect.</p>
      </div>
    {:else}
      <h2 id="tls-dlg-t" class="m-0 text-[18px] font-bold">Replace the CA?</h2>
      <div id="tls-dlg-d" class="mt-2 text-[14.5px]">
        <p class="m-0">
          Woodshed makes a new CA and a new certificate. <b>Every device must import the new CA certificate again before it can connect.</b> Until then it
          can't send logs over TLS.
        </p>
        <p class="warnp">
          Some firewalls block traffic through them while their TLS log host can't be reached or its certificate fails. Update each one straight away, or
          check that it permits traffic while the log host is down.
        </p>
        <p class="mt-2 mb-0 text-[13.5px] text-muted">
          Do this if the CA's key may have been copied. For a new certificate that devices accept as they are, use Renew certificate instead.
        </p>
        <label class="mt-3 flex items-start gap-2 text-[14px]">
          <input type="checkbox" class="mt-1" bind:checked={understood} />
          <span>I'll import the new CA certificate on every device.</span>
        </label>
      </div>
    {/if}
    {#if actionError}<p class="mt-2 mb-0 text-[13.5px] font-medium text-bad-fg" role="alert">{actionError}</p>{/if}
    <div class="mt-4 flex justify-end gap-2">
      <button type="button" bind:this={cancelBtn} class="ghost-btn" onclick={() => (confirm = null)} disabled={busy}>Cancel</button>
      {#if confirm === 'renew'}
        <button type="button" class="pill-btn" onclick={renew} disabled={busy}>{busy ? 'Renewing…' : 'Renew certificate'}</button>
      {:else}
        <button type="button" class="pill-btn" onclick={renew} disabled={busy || !understood}>{busy ? 'Replacing…' : 'Replace CA'}</button>
      {/if}
    </div>
  </div>
{/if}

<style>
  .hint {
    font-size: 13px;
    color: var(--muted);
    margin-bottom: 10px;
  }
  .empty {
    padding: 12px 14px;
    border-radius: 14px;
    border: 1px dashed var(--input-border);
  }
  .certs {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 14px;
    scroll-margin-top: 90px;
  }
  @media (max-width: 900px) {
    .certs {
      grid-template-columns: 1fr;
    }
  }
  .cert {
    padding: 12px 14px;
    border-radius: 16px;
    border: 1px solid var(--input-border);
    min-width: 0;
  }
  .ch {
    margin: 0 0 6px;
    font-size: 14.5px;
    font-weight: 700;
  }
  .ch .sub {
    font-weight: 400;
    font-size: 12.5px;
    color: var(--muted);
    margin-left: 6px;
  }
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 3px 12px;
    margin: 0 0 6px;
    font-size: 13.5px;
  }
  dt {
    color: var(--muted);
  }
  dd {
    margin: 0;
    min-width: 0;
  }
  .soon {
    color: var(--warn-fg);
    font-weight: 600;
  }
  .fp-row {
    display: grid;
    grid-template-columns: 62px 1fr auto;
    align-items: center;
    gap: 8px;
    margin-top: 6px;
  }
  .fp-label {
    font-size: 12.5px;
    color: var(--muted);
  }
  .fp {
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.45;
    overflow-wrap: anywhere;
  }
  .dl {
    display: grid;
    grid-template-columns: 190px 1fr;
    align-items: center;
    gap: 10px;
    margin-top: 6px;
  }
  @media (max-width: 1100px) {
    .dl {
      grid-template-columns: 1fr;
      gap: 2px;
    }
  }
  .names {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .names li {
    padding: 2px 9px;
    border-radius: 999px;
    background: var(--chip);
    border: 1px solid var(--border);
    font-size: 12.5px;
  }
  .names code {
    font-family: var(--font-mono);
  }
  .missing {
    margin: 8px 0 0;
    padding: 8px 12px;
    border-radius: 12px;
    border: 1px solid var(--warn-border);
    background: var(--warn-bg);
    font-size: 13px;
  }
  .renew {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 10px;
    margin-top: 12px;
  }
  .done {
    margin: 0 0 10px;
    padding: 8px 12px;
    border-radius: 12px;
    border: 1px solid var(--info-border);
    background: var(--info-bg);
    font-size: 13.5px;
  }
  .fwbox {
    margin-bottom: 12px;
    padding: 10px 14px;
    border-radius: 14px;
    border: 1px solid var(--warn-border);
    background: var(--warn-bg);
  }
  .trouble {
    margin-top: 14px;
    scroll-margin-top: 90px;
  }
  .trouble summary {
    cursor: pointer;
    font-size: 14px;
    font-weight: 600;
  }
  .trouble summary::marker {
    color: var(--muted);
  }
  .trouble ul {
    margin: 8px 0 10px;
    padding-left: 20px;
    list-style: disc;
    font-size: 13.5px;
    line-height: 1.55;
  }
  .trouble code,
  .hint code {
    font-family: var(--font-mono);
    font-size: 12.5px;
  }
  .cmd {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 8px 8px 12px;
    border-radius: 14px;
  }
  .cmd code {
    flex: 1;
    font-family: var(--font-mono);
    font-size: 12.5px;
    overflow-wrap: anywhere;
  }
  td.detail {
    overflow-wrap: break-word;
    min-width: 220px;
  }
  #tls-failures {
    scroll-margin-top: 90px;
  }
  .ferr {
    margin: 4px 0 0;
    font-size: 13px;
    font-weight: 500;
    color: var(--bad-fg);
  }
  th {
    text-align: left;
    font-weight: 600;
    color: var(--muted);
    padding: 6px 10px;
    border-bottom: 1px solid var(--divider);
    position: sticky;
    top: 0;
    background: var(--glass-strong);
  }
  td {
    padding: 6px 10px;
    border-bottom: 1px solid var(--divider);
    vertical-align: top;
  }
  .warnp {
    margin: 10px 0 0;
    padding: 8px 12px;
    border-radius: 12px;
    border: 1px solid var(--warn-border);
    background: var(--warn-bg);
    font-size: 13.5px;
  }
  .scrim {
    position: fixed;
    inset: 0;
    z-index: 70;
    background: rgba(10, 12, 16, 0.45);
  }
  .dialog {
    position: fixed;
    z-index: 71;
    left: 50%;
    top: 30%;
    transform: translate(-50%, -30%);
    width: min(500px, calc(100vw - 32px));
    padding: 22px;
    border-radius: 22px;
    background: var(--glass-strong);
    box-shadow: var(--shadow);
    -webkit-backdrop-filter: blur(20px);
    backdrop-filter: blur(20px);
  }
</style>
