<script>
  import { onMount } from 'svelte'
  import TunnelPort from './TunnelPort.svelte'
  import { when } from '../lib/format.js'
  import { t } from '../i18n/i18n.svelte.js'

  // info comes from App (also used by the restart banner); refreshed on open.
  let { info = null, error = '', hubs = [], toast, onchange } = $props()
  onMount(() => onchange())
  const list = (l) => (l && l.length ? l.join(', ') : '—')
</script>

<div class="card">
  <h2>{t('nav.server')}</h2>
  {#if error}<div class="banner bad">{error}</div>{/if}
  {#if info}
    <dl>
      <dt>{t('info.version')}</dt><dd>{info.version}</dd>
      <dt>{t('info.started')}</dt><dd>{when(info.started)}</dd>
      <dt>{t('info.ports', { proto: 'UDP' })}</dt><dd>{list(info.udp_ports)}</dd>
      <dt>{t('info.ports', { proto: 'TCP' })}</dt><dd>{list(info.tcp_ports)}</dd>
      <dt>{t('info.ports', { proto: 'WSS' })}</dt><dd>{list(info.wss_ports)} {info.wss_path}</dd>
      <dt>{t('info.tunnelPort')}</dt>
      <TunnelPort {info} {hubs} {toast} {onchange} />
      <dt>{t('info.endpoints')}</dt>
      <dd>{list(info.endpoints)}{#if info.endpoints_err}<div class="banner bad">{info.endpoints_err}</div>{/if}</dd>
      <dt>{t('info.minHub')}</dt><dd>{info.min_hub_version || t('info.any')}</dd>
      <dt>{t('info.pep')}</dt><dd>{info.pep ? t('info.on') : t('info.off')}</dd>
      <dt>{t('info.tunnelNet')}</dt><dd>{info.tunnel_prefix}</dd>
      <dt>{t('info.tokenTtl')}</dt><dd>{info.token_ttl}</dd>
      <dt>{t('info.egress')}</dt><dd>{list(info.egress_allow)}</dd>
      <dt>{t('info.cert')}</dt><dd><code>{info.web_fingerprint}</code></dd>
    </dl>
  {/if}
</div>

<style>
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 6px 16px;
    margin: 0;
  }
  dt {
    color: var(--muted);
  }
  dd {
    margin: 0;
    word-break: break-all;
  }
</style>
