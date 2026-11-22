<script>
  // Tunnel port: shown from the server info and changed where it is
  // configured (HA app Network section or server.yaml) through the API.
  import ConfirmDialog from './ConfirmDialog.svelte'
  import { api } from '../lib/api.js'

  let { info, hubs = [], toast, onchange } = $props()
  let value = $state('')
  let confirming = $state(null)

  const where = {
    ha: 'sekcja Sieć aplikacji w Home Assistant',
    file: 'plik server.yaml (public_port)',
    env: 'zmienna XS_SERVER_PUBLIC_PORT (zmień ją w .env i uruchom serwer ponownie)',
    default: 'domyślny (serwer bez pliku konfiguracji)',
  }

  function ask() {
    const port = Number(value)
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      toast('Port musi być liczbą od 1 do 65535', true)
      return
    }
    if (port === info.tunnel_port_configured) {
      toast('To jest już ustawiony port')
      return
    }
    confirming = port
  }

  async function save() {
    const port = confirming
    confirming = null
    try {
      await api.tunnelPort(port)
      toast(`Port tunelu ustawiony na ${port}: uruchom serwer ponownie`)
      value = ''
    } catch (err) {
      toast(err.message, true)
    }
    onchange()
  }
</script>

<dd>
  <b>{info.tunnel_port}</b>
  {#if info.tunnel_port_configured !== info.tunnel_port}
    → {info.tunnel_port_configured} po restarcie
  {/if}
  <div class="muted small">
    Port, z którym huby łączą się z internetu (UDP i TCP). Ustawienie: {where[info.tunnel_port_source] ||
      info.tunnel_port_source}.
  </div>
  {#if info.tunnel_port_err}<div class="banner bad">{info.tunnel_port_err}</div>{/if}
  {#if info.tunnel_port_writable}
    <div class="change">
      <input type="number" min="1" max="65535" placeholder={String(info.tunnel_port_configured)} bind:value />
      <button onclick={ask} disabled={!value}>Zmień port</button>
    </div>
  {/if}
  {#if info.reenroll?.length}
    <div class="banner bad">Zarejestruj ponownie (inny port w kodzie): {info.reenroll.join(', ')}</div>
  {/if}
</dd>

{#if confirming}
  <ConfirmDialog title="Zmienić port tunelu na {confirming}?" yes="Zmień port" no="Anuluj" danger={hubs.length > 0}
    onyes={save} onno={() => (confirming = null)}>
    <ul>
      <li>Zmiana zostanie zapisana w: {where[info.tunnel_port_source]}. Zacznie działać po restarcie serwera.</li>
      <li>
        Na routerze przekieruj port <b>{confirming}</b> (UDP i TCP) na ten serwer, w miejsce portu
        {info.tunnel_port}. Szczegóły: README, sekcja „Tunnel port”.
      </li>
      {#if hubs.length > 0}
        <li>
          <b>Uwaga:</b> zarejestrowane huby ({hubs.length}) mają w kodzie port {info.tunnel_port}. Po restarcie nie
          połączą się, dopóki nie zarejestrujesz ich ponownie (Usuń → Dodaj hub → nowy kod w hubie). Telefony nie
          wymagają zmian.
        </li>
      {/if}
    </ul>
  </ConfirmDialog>
{/if}

<style>
  dd {
    margin: 0;
  }
  .change {
    display: flex;
    gap: 8px;
    margin-top: 6px;
  }
  .change input {
    width: 110px;
  }
  ul {
    margin: 0;
    padding-left: 18px;
  }
  li + li {
    margin-top: 6px;
  }
</style>
