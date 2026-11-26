<script>
  // Tunnel port: shown from the server info and changed where it is
  // configured (HA app Network section or server.yaml) through the API.
  import ConfirmDialog from './ConfirmDialog.svelte'
  import { api } from '../lib/api.js'
  import { t } from '../i18n/i18n.svelte.js'

  let { info, hubs = [], toast, onchange } = $props()
  let value = $state('')
  let confirming = $state(null)

  const sources = ['ha', 'file', 'env', 'default']
  const where = (s) => (sources.includes(s) ? t('port.where.' + s) : s)

  function ask() {
    const port = Number(value)
    if (!Number.isInteger(port) || port < 1 || port > 65535) {
      toast(t('port.invalid'), true)
      return
    }
    if (port === info.tunnel_port_configured) {
      toast(t('port.same'))
      return
    }
    confirming = port
  }

  async function save() {
    const port = confirming
    confirming = null
    try {
      await api.tunnelPort(port)
      toast(t('port.saved', { port }))
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
    → {t('port.afterRestart', { port: info.tunnel_port_configured })}
  {/if}
  <div class="muted small">
    {t('port.hint', { where: where(info.tunnel_port_source) })}
  </div>
  {#if info.tunnel_port_err}<div class="banner bad">{info.tunnel_port_err}</div>{/if}
  {#if info.tunnel_port_writable}
    <div class="change">
      <input type="number" min="1" max="65535" placeholder={String(info.tunnel_port_configured)} bind:value />
      <button onclick={ask} disabled={!value}>{t('port.change')}</button>
    </div>
  {/if}
  {#if info.reenroll?.length}
    <div class="banner bad">{t('port.reenroll', { names: info.reenroll.join(', ') })}</div>
  {/if}
</dd>

{#if confirming}
  <ConfirmDialog title={t('port.ask', { port: confirming })} yes={t('port.change')} no={t('common.cancel')} danger={hubs.length > 0}
    onyes={save} onno={() => (confirming = null)}>
    <ul>
      <li>{t('port.ask1', { where: where(info.tunnel_port_source) })}</li>
      <li>
        {t('port.ask2a')} <b>{confirming}</b> {t('port.ask2b', { old: info.tunnel_port })}
      </li>
      {#if hubs.length > 0}
        <li>
          <b>{t('port.warn')}</b> {t('port.ask3', { n: hubs.length, old: info.tunnel_port })}
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
