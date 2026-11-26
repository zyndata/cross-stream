<script>
  import { onMount } from 'svelte'
  import { api, errorText, enrollErrorText } from '../lib/api.js'
  import { pathHint } from '../lib/fmt.js'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import BypassCard from './BypassCard.svelte'
  import ForwardsCard from './ForwardsCard.svelte'
  import NotifyCard from './NotifyCard.svelte'
  import CloseCard from './CloseCard.svelte'
  import DiagCard from './DiagCard.svelte'
  import UpdateCard from './UpdateCard.svelte'
  import LangCard from './LangCard.svelte'
  import { t } from '../i18n/i18n.svelte.js'

  let { view, notify } = $props()
  let hs = $state(null)
  let form = $state({ ssid: '', psk: '', band: 'a', channel: 0, country: 'PL', client_wifi: '' })
  let showPsk = $state(false)
  let busy = $state(false)
  let code = $state('')
  let enrolled = $state(null)
  let confirmStop = $state(false)

  async function load() {
    try {
      hs = await api.hotspot()
      form = { ...hs.settings }
    } catch (e) {
      notify(errorText(e), true)
    }
  }
  // Windows: Mobile Hotspot picks channel and regulatory domain itself.
  let windows = $state(false)
  onMount(async () => {
    windows = (await api.platform().catch(() => '')) === 'windows'
  })
  onMount(load)

  // channelWhy turns xs-hubd's channel_why (English) into the UI language.
  function channelWhy(why) {
    if (!why) return ''
    if (why === 'configured') return t('hotspot.why.configured')
    if (why.startsWith('auto:')) return t('hotspot.why.auto')
    if (why.startsWith('first usable')) return t('hotspot.why.first')
    if (why === 'chosen by Windows') return t('hotspot.why.windows')
    if (why.startsWith('next to Wi-Fi')) return t('hotspot.why.nextTo')
    return t('hotspot.why.nm')
  }

  async function run(fn) {
    busy = true
    try {
      await fn()
    } catch (e) {
      notify(errorText(e), true)
    } finally {
      busy = false
    }
  }

  const saveHotspot = () =>
    run(async () => {
      const changedLogin = form.ssid !== hs.settings.ssid || form.psk !== hs.settings.psk
      hs = await api.setHotspot({ ...form, channel: Number(form.channel) || 0 })
      form = { ...hs.settings }
      notify(changedLogin ? t('hotspot.savedRepair') : t('hotspot.saved'))
    })

  const power = (on) =>
    run(async () => {
      confirmStop = false
      hs = await api.hotspotPower(on)
      notify(on ? t('hotspot.started') : t('hotspot.stopped'))
    })

  const enroll = () =>
    run(async () => {
      const again = view.enrolled
      try {
        enrolled = await api.enroll(code.trim())
      } catch (e) {
        throw new Error(enrollErrorText(e))
      }
      code = ''
      if (enrolled.pending) {
        notify(t('enroll.pendingToast'))
        return
      }
      notify(again ? t('enroll.againToast') : t('enroll.doneToast'))
    })
</script>

<div class="grid">
  <div class="card">
    <h2>{t('hotspot.title')}</h2>
    {#if !hs}
      <p class="muted">{t('common.loading')}</p>
    {:else if !hs.enabled}
      <p class="muted">{t('hotspot.headless')}</p>
    {:else}
      <p>
        {t('hotspot.state')}
        {#if hs.on}
          <b class="ok">{t('hotspot.running')}</b> · {hs.state.band === 'a' ? '5 GHz' : hs.state.band === 'bg' ? t('hotspot.band24') : t('hotspot.bandAuto')}{#if !windows}, {t('hotspot.channel', { n: hs.state.channel })}{/if}
          {#if channelWhy(hs.state.channel_why)}({channelWhy(hs.state.channel_why)}){/if}
          · {t('hotspot.clients', { n: hs.clients })}
        {:else}
          <b class="bad">{t('hotspot.stoppedState')}</b>
        {/if}
      </p>
      {#if hs.on && hs.state.scan}<p class="muted small">{t('hotspot.scan', { scan: hs.state.scan })}</p>{/if}
      {#if hs.error}<p class="banner bad">{hs.error}</p>{/if}
      {#each hs.state.warnings ?? [] as w}<p class="banner warn">{w}</p>{/each}
      <form onsubmit={(e) => (e.preventDefault(), saveHotspot())}>
        <label>{t('hotspot.ssid')} <input bind:value={form.ssid} maxlength="32" required /></label>
        <label>
          {t('hotspot.password')}
          <span class="pw">
            <input type={showPsk ? 'text' : 'password'} bind:value={form.psk} minlength="8" maxlength="63" required />
            <button type="button" onclick={() => (showPsk = !showPsk)}>{showPsk ? t('common.hide') : t('common.show')}</button>
          </span>
        </label>
        <label>
          {t('hotspot.band')}
          <select bind:value={form.band}>
            <option value="a">{t('hotspot.band5')}</option>
            <option value="bg">{t('hotspot.band24')}</option>
          </select>
        </label>
        {#if !windows}
          <label>{t('hotspot.channelInput')} <input type="number" min="0" max="196" bind:value={form.channel} /></label>
          <label>{t('hotspot.country')} <input bind:value={form.country} maxlength="2" /></label>
          <label>
            {t('hotspot.clientWifi')}
            <select bind:value={form.client_wifi}>
              <option value="">{t('hotspot.clientWifi.ask')}</option>
              <option value="share">{t('hotspot.clientWifi.share')}</option>
              <option value="disconnect">{t('hotspot.clientWifi.disconnect')}</option>
            </select>
          </label>
        {/if}
        <p class="muted small">{t('hotspot.restartHint')}</p>
        <div class="row">
          <button class="primary" disabled={busy}>{t('hotspot.saveRestart')}</button>
          {#if hs.on}
            <button type="button" class="danger" disabled={busy} onclick={() => (confirmStop = true)}>
              {t('hotspot.stop')}
            </button>
          {:else}
            <button type="button" disabled={busy} onclick={() => power(true)}>{t('hotspot.start')}</button>
          {/if}
        </div>
      </form>
    {/if}
  </div>
  {#if confirmStop}
    <ConfirmDialog title={t('hotspot.stopAsk')} yes={t('hotspot.stopYes')} no={t('common.no')} danger
      onyes={() => power(false)} onno={() => (confirmStop = false)}>
      <ul>
        <li>{t('hotspot.stopWhat1', { ssid: hs.settings.ssid, phones: view.relays.filter((r) => r.connected).length, devices: hs.clients })}</li>
        <li>{t('hotspot.stopWhat2')}</li>
        <li>{t('hotspot.stopWhat3')}</li>
      </ul>
    </ConfirmDialog>
  {/if}

  <div class="card">
    <h2>{t('server.title')}</h2>
    {#if view.enrolled}
      <p>
        {view.enroll_pending ? t('server.pending') : t('server.enrolled')}{#if view.server_ep}: <code>{view.server_ep}</code>{/if}
      </p>
      <p class="muted small">{t('server.tunnel', { state: view.tunnel_up ? t('server.tunnelUp') : t('server.tunnelDown') })}{view.tunnel_error ? ` (${view.tunnel_error})` : ''}</p>
    {:else}
      <p>{t('server.notYet')}</p>
    {/if}
    <form onsubmit={(e) => (e.preventDefault(), enroll())}>
      <label>
        {t('server.codeLabel')}
        <textarea bind:value={code} rows="3" placeholder="XS1.…" spellcheck="false"></textarea>
      </label>
      <p class="muted small">{pathHint(view)}</p>
      {#if view.enrolled}
        <p class="muted small">{t('server.againHint')}</p>
      {/if}
      <button class="primary" disabled={busy || !code.trim().startsWith('XS1.')}>
        {busy ? t('enroll.busy') : view.enrolled ? t('enroll.again') : t('enroll.button')}
      </button>
    </form>
    {#if enrolled?.pending}
      <div class="banner result">
        {t('enroll.switchedAs')} <b>{enrolled.name}</b>{t('enroll.switchedRest')}
        <br />{t('enroll.serverList', { list: enrolled.endpoints?.join(', ') })}
      </div>
    {:else if enrolled}
      <div class="banner result">
        {t('enroll.doneAs')} <b>{enrolled.name}</b>{t('enroll.doneAddr')} <code>{enrolled.tunnel_ip}</code>, MTU {enrolled.mtu}.
        <br />{t('enroll.serverList', { list: enrolled.endpoints?.join(', ') })}
      </div>
    {/if}
  </div>

  {#if view.own_traffic !== undefined}
    <div class="card">
      <h2>{t('own.title')}</h2>
      <div class="seg">
        <button class:active={!view.own_traffic} disabled={busy || windows}
          onclick={() => run(async () => (await api.setOwnTraffic(false), notify(t('own.offToast'))))}>{t('common.off')}</button>
        <button class:active={view.own_traffic} disabled={busy || windows}
          onclick={() => run(async () => (await api.setOwnTraffic(true), notify(t('own.onToast'))))}>{t('common.on')}</button>
      </div>
      {#if windows}
        <p class="muted small">{t('own.windows')}</p>
      {:else}
        <p class="muted small">{t('own.hint')}</p>
        {#if view.own_traffic && view.own_traffic_dns === 'system'}
          <p class="muted small warn">{t('own.noResolved')}</p>
        {/if}
      {/if}
    </div>
  {/if}

  <BypassCard {notify} />
  {#if windows}
    <section class="card"><h2>{t('fwd.title')}</h2><p class="muted">{t('fwd.windows')}</p></section>
  {:else}
    <ForwardsCard {notify} />
  {/if}
  <LangCard {notify} />
  <NotifyCard {notify} />
  <CloseCard {notify} />
  <UpdateCard {notify} />
  <DiagCard {view} {notify} />
</div>

<style>
  .seg {
    display: inline-flex;
    gap: 0;
  }
  .seg button {
    border-radius: 0;
  }
  .seg button:first-child {
    border-radius: 6px 0 0 6px;
  }
  .seg button:last-child {
    border-radius: 0 6px 6px 0;
  }
  .seg button.active {
    background: var(--accent);
    border-color: var(--accent);
    color: #fff;
  }
  p.warn {
    color: var(--warn, #c77700);
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(380px, 1fr));
    gap: 14px;
  }
  form {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 13px;
    color: var(--muted);
  }
  label input,
  label select,
  label textarea {
    color: var(--text);
  }
  .pw {
    display: flex;
    gap: 6px;
  }
  .pw input {
    flex: 1;
  }
  .row {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
  }
  .ok {
    color: var(--ok);
  }
  .bad {
    color: var(--bad);
  }
  ul {
    padding-left: 18px;
    margin: 0;
  }
  li {
    margin: 4px 0;
  }
  .result {
    margin-top: 12px;
    background: color-mix(in srgb, var(--ok) 14%, transparent);
  }
  textarea {
    font-family: ui-monospace, monospace;
    font-size: 12px;
  }
</style>
