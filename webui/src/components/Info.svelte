<script>
  import { onMount } from 'svelte'
  import TunnelPort from './TunnelPort.svelte'
  import { when } from '../lib/format.js'

  // info comes from App (also used by the restart banner); refreshed on open.
  let { info = null, error = '', hubs = [], toast, onchange } = $props()
  onMount(() => onchange())
  const list = (l) => (l && l.length ? l.join(', ') : '—')
</script>

<div class="card">
  <h2>Serwer</h2>
  {#if error}<div class="banner bad">{error}</div>{/if}
  {#if info}
    <dl>
      <dt>Wersja</dt><dd>{info.version}</dd>
      <dt>Uruchomiony</dt><dd>{when(info.started)}</dd>
      <dt>Porty UDP</dt><dd>{list(info.udp_ports)}</dd>
      <dt>Porty TCP</dt><dd>{list(info.tcp_ports)}</dd>
      <dt>Porty WSS</dt><dd>{list(info.wss_ports)} {info.wss_path}</dd>
      <dt>Port tunelu</dt>
      <TunnelPort {info} {hubs} {toast} {onchange} />
      <dt>Adresy w kodach rejestracji</dt>
      <dd>{list(info.endpoints)}{#if info.endpoints_err}<div class="banner bad">{info.endpoints_err}</div>{/if}</dd>
      <dt>Minimalna wersja huba</dt><dd>{info.min_hub_version || 'dowolna'}</dd>
      <dt>PEP (przyspieszanie TCP)</dt><dd>{info.pep ? 'włączony' : 'wyłączony'}</dd>
      <dt>Sieć tunelu</dt><dd>{info.tunnel_prefix}</dd>
      <dt>Ważność kodu rejestracji</dt><dd>{info.token_ttl}</dd>
      <dt>Wyjątki filtra ruchu</dt><dd>{list(info.egress_allow)}</dd>
      <dt>Certyfikat panelu (SHA-256)</dt><dd><code>{info.web_fingerprint}</code></dd>
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
