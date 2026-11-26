<script>
  // "Restart required" on every page: the configured tunnel port differs from
  // the one in use. The panel never keeps the port itself (server info only).
  import ConfirmDialog from './ConfirmDialog.svelte'
  import { api } from '../lib/api.js'
  import { t } from '../i18n/i18n.svelte.js'

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
    toast(t('restart.restarting'))
    // The panel is down while the server restarts: poll until a new start.
    for (let i = 0; i < 60; i++) {
      await new Promise((r) => setTimeout(r, 3000))
      try {
        const now = await api.info()
        if (now.started !== started) {
          waiting = false
          toast(t('restart.done'))
          onchange()
          return
        }
      } catch {
        // still restarting
      }
    }
    waiting = false
    toast(t('restart.noAnswer'), true)
  }
</script>

{#if info?.restart_required}
  <div class="banner warn restart">
    <div>
      <b>{t('restart.required')}</b> {t('restart.body', { old: info.tunnel_port, new: info.tunnel_port_configured })}
    </div>
    {#if info.can_restart}
      <button class="primary" disabled={waiting} onclick={() => (asking = true)}>
        {waiting ? t('restart.running') : t('restart.button')}
      </button>
    {:else}
      <div class="small">{t('restart.manual')}</div>
    {/if}
  </div>
{/if}

{#if asking}
  <ConfirmDialog title={t('restart.ask')} yes={t('restart.button')} no={t('common.cancel')} onyes={restart} onno={() => (asking = false)}>
    {t('restart.askBody', { new: info.tunnel_port_configured, old: info.tunnel_port })}
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
