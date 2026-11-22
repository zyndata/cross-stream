<script>
  // "Restart required" on every page: the configured tunnel port differs from
  // the one in use. The panel never keeps the port itself (server info only).
  import ConfirmDialog from './ConfirmDialog.svelte'
  import { api } from '../lib/api.js'

  let { info = null, toast, onchange } = $props()
  let asking = $state(false)
  let waiting = $state(false)

  async function restart() {
    asking = false
    const started = info?.started
    try {
      await api.restart()
    } catch (err) {
      toast(err.message, true)
      return
    }
    waiting = true
    toast('Serwer uruchamia się ponownie…')
    // The panel is down while the server restarts: poll until a new start.
    for (let i = 0; i < 60; i++) {
      await new Promise((r) => setTimeout(r, 3000))
      try {
        const now = await api.info()
        if (now.started !== started) {
          waiting = false
          toast('Serwer działa po restarcie')
          onchange()
          return
        }
      } catch {
        // still restarting
      }
    }
    waiting = false
    toast('Serwer nie odpowiada po restarcie: sprawdź jego log', true)
  }
</script>

{#if info?.restart_required}
  <div class="banner warn restart">
    <div>
      <b>Wymagany restart serwera.</b> Port tunelu zmieniony z {info.tunnel_port} na {info.tunnel_port_configured};
      do restartu serwer działa na porcie {info.tunnel_port}.
    </div>
    {#if info.can_restart}
      <button class="primary" disabled={waiting} onclick={() => (asking = true)}>
        {waiting ? 'Trwa restart…' : 'Uruchom ponownie'}
      </button>
    {:else}
      <div class="small">Uruchom serwer ponownie ręcznie.</div>
    {/if}
  </div>
{/if}

{#if asking}
  <ConfirmDialog title="Uruchomić serwer ponownie?" yes="Uruchom ponownie" no="Anuluj" onyes={restart} onno={() => (asking = false)}>
    Wszystkie huby rozłączą się na kilkanaście sekund i połączą same — o ile router przekierowuje już nowy port
    {info.tunnel_port_configured}. Huby zarejestrowane z portem {info.tunnel_port} trzeba potem zarejestrować ponownie.
  </ConfirmDialog>
{/if}

<style>
  .restart {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 16px;
    margin-bottom: 12px;
  }
  .restart div {
    flex: 1 1 260px;
  }
</style>
