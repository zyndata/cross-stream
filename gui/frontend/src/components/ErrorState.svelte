<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  import { t } from '../i18n/i18n.svelte.js'
  let { view } = $props()
  // Windows: the service is CrossStreamHub (sc.exe), no sudo/systemctl.
  let windows = $state(false)
  let control = $state(false) // the GUI may start the service itself
  onMount(async () => {
    windows = (await api.platform().catch(() => '')) === 'windows'
    control = await api.serviceControl().catch(() => false)
  })
  const start = () => api.startHub().catch(() => {})
  // Wi-Fi choice: xs-hubd waits until the owner picks one (saved in the hub).
  let choosing = $state(false)
  let choiceErr = $state('')
  async function choose(choice) {
    choosing = true
    choiceErr = ''
    try {
      await api.clientWifiChoice(choice)
    } catch (e) {
      choiceErr = String(e?.message ?? e)
    } finally {
      choosing = false
    }
  }
  const tried = (v) => (v === 'share' || v === 'disconnect' ? t('wifiAsk.tried.' + v) : v)
</script>

<div class="card err">
  {#if view.state === 'wifi' && view.client_wifi}
    {@const q = view.client_wifi}
    <h2>{t('wifiAsk.title', { conn: q.conn })}</h2>
    <p>{t('wifiAsk.body1')} <code>{q.iface}</code> {t('wifiAsk.body2')}</p>
    {#if q.error}
      <p class="banner bad">{t('wifiAsk.failed', { what: tried(q.tried) })} {q.error}</p>
    {/if}
    <div class="choices">
      {#if q.can_share}
        <div>
          <button class="primary" disabled={choosing} onclick={() => choose('share')}>{t('wifiAsk.share', { conn: q.conn })}</button>
          <p class="muted small">{t('wifiAsk.shareHint')}</p>
        </div>
      {/if}
      <div>
        <button class:primary={!q.can_share} disabled={choosing} onclick={() => choose('disconnect')}>{t('wifiAsk.disconnect')}</button>
        <p class="muted small">{t('wifiAsk.disconnectHint', { conn: q.conn })}</p>
      </div>
    </div>
    {#if !q.can_share}
      <p class="muted small">{t('wifiAsk.cannotShare')}</p>
    {/if}
    {#if choiceErr}<p class="banner bad">{choiceErr}</p>{/if}
  {:else if view.state === 'group'}
    <h2>{t('state.group.title')}</h2>
    <p>{t('state.group.body1')} <code>crossstream</code>{t('state.group.body2')}</p>
    <ol>
      {#if windows}
        <li>
          {t('state.group.win1')} (<code>install.ps1</code>) {t('state.group.win2')}
          <code>Add-LocalGroupMember crossstream -Member {t('state.group.winName')}</code>
        </li>
      {:else}
        <li>{t('state.group.linux')} <code>sudo usermod -aG crossstream $USER</code></li>
      {/if}
      <li>{t('state.group.relogin')}</li>
    </ol>
  {:else if view.state === 'daemon' && view.starting}
    <h2>{t('state.starting.title')}</h2>
    <p>{t('state.panelOnly')} <code>xs-hubd</code>{t('state.starting.body')}</p>
  {:else if view.state === 'daemon'}
    {#if view.lost}
      <h2>{t('state.lost.title')}</h2>
      <p>{t('state.lost.body')}</p>
    {:else}
      <h2>{t('state.down.title')}</h2>
      <p>{t('state.panelOnly')} <code>xs-hubd</code>.</p>
    {/if}
    {#if control}<p><button class="primary" onclick={start}>{t('state.start')}</button></p>{/if}
    {#if windows}
      <p>{t('state.win.auto1')} <code>CrossStreamHub</code>{t('state.win.auto2')}</p>
      <ol>
        <li>{t('state.win.manual')} <code>Start-Service CrossStreamHub</code></li>
        <li>{t('state.logs')} <code>C:\ProgramData\CrossStream\xs-hubd.log</code> {t('state.win.logsAdmin')}</li>
      </ol>
    {:else}
      <ol>
        <li>{t('state.linux.start')} <code>sudo systemctl start xs-hubd</code></li>
        <li>{t('state.linux.logs')} <code>systemctl status xs-hubd</code>, <code>journalctl -u xs-hubd -e</code></li>
      </ol>
    {/if}
  {:else}
    <h2>{t('state.error.title')}</h2>
  {/if}
  {#if view.error}<p class="muted small">{t('state.details', { error: view.error })}</p>{/if}
  {#if view.state !== 'wifi'}<p class="muted small">{t('state.retry')}</p>{/if}
</div>

<style>
  .err {
    max-width: 720px;
    margin: 40px auto;
    padding: 22px 26px;
  }
  li {
    margin: 6px 0;
  }
  .choices {
    display: grid;
    gap: 12px;
    margin: 16px 0;
  }
  .choices p {
    margin: 4px 0 0;
  }
</style>
