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

  // channelWhy turns xs-hubd's channel_why (English) into Polish.
  function channelWhy(why) {
    if (!why) return ''
    if (why === 'configured') return 'ustawiony ręcznie'
    if (why.startsWith('auto:')) return 'wybrany automatycznie: najmniej zajęty'
    if (why.startsWith('first usable')) return 'pierwszy dozwolony, bez skanowania'
    if (why === 'chosen by Windows') return 'wybiera Windows'
    if (why.startsWith('next to Wi-Fi')) return 'obok sieci Wi-Fi laptopa, na jej kanale'
    return 'wybiera NetworkManager'
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
      notify(changedLogin ? 'Zapisano. Telefony trzeba sparować ponownie (nowy kod QR).' : 'Zapisano ustawienia hotspotu')
    })

  const power = (on) =>
    run(async () => {
      confirmStop = false
      hs = await api.hotspotPower(on)
      notify(on ? 'Hotspot uruchomiony' : 'Hotspot zatrzymany')
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
      notify(again ? 'Hub zarejestrowany ponownie — usługa uruchamia się od nowa' : 'Hub zarejestrowany na serwerze')
    })
</script>

<div class="grid">
  <div class="card">
    <h2>Hotspot Wi-Fi</h2>
    {#if !hs}
      <p class="muted">Ładowanie…</p>
    {:else if !hs.enabled}
      <p class="muted">xs-hubd działa bez hotspotu (tryb headless).</p>
    {:else}
      <p>
        Stan:
        {#if hs.on}
          <b class="ok">działa</b> · {hs.state.band === 'a' ? '5 GHz' : hs.state.band === 'bg' ? '2,4 GHz' : 'pasmo automatyczne'}{#if !windows}, kanał {hs.state.channel}{/if}
          {#if channelWhy(hs.state.channel_why)}({channelWhy(hs.state.channel_why)}){/if}
          · klienci: {hs.clients}
        {:else}
          <b class="bad">zatrzymany</b>
        {/if}
      </p>
      {#if hs.on && hs.state.scan}<p class="muted small">Sąsiednie sieci (kanał: liczba, najsilniejsza): {hs.state.scan}</p>{/if}
      {#if hs.error}<p class="banner bad">{hs.error}</p>{/if}
      {#each hs.state.warnings ?? [] as w}<p class="banner warn">{w}</p>{/each}
      <form onsubmit={(e) => (e.preventDefault(), saveHotspot())}>
        <label>Nazwa sieci (SSID) <input bind:value={form.ssid} maxlength="32" required /></label>
        <label>
          Hasło
          <span class="pw">
            <input type={showPsk ? 'text' : 'password'} bind:value={form.psk} minlength="8" maxlength="63" required />
            <button type="button" onclick={() => (showPsk = !showPsk)}>{showPsk ? 'Ukryj' : 'Pokaż'}</button>
          </span>
        </label>
        <label>
          Pasmo
          <select bind:value={form.band}>
            <option value="a">5 GHz (gdy niedostępne: 2,4 GHz)</option>
            <option value="bg">2,4 GHz</option>
          </select>
        </label>
        {#if !windows}
          <label>Kanał (0 = automatycznie) <input type="number" min="0" max="196" bind:value={form.channel} /></label>
          <label>Kraj (kod regulacyjny) <input bind:value={form.country} maxlength="2" /></label>
          <label>
            Gdy karta Wi-Fi jest połączona z siecią
            <select bind:value={form.client_wifi}>
              <option value="">zapytaj przy starcie</option>
              <option value="share">hotspot obok tej sieci (na jej kanale)</option>
              <option value="disconnect">rozłącz Wi-Fi laptopa</option>
            </select>
          </label>
        {/if}
        <p class="muted small">Zmiana nazwy lub hasła restartuje hotspot — telefony trzeba sparować ponownie.</p>
        <div class="row">
          <button class="primary" disabled={busy}>Zapisz i uruchom ponownie</button>
          {#if hs.on}
            <button type="button" class="danger" disabled={busy} onclick={() => (confirmStop = true)}>
              Zatrzymaj hotspot
            </button>
          {:else}
            <button type="button" disabled={busy} onclick={() => power(true)}>Uruchom hotspot</button>
          {/if}
        </div>
      </form>
    {/if}
  </div>
  {#if confirmStop}
    <ConfirmDialog title="Zatrzymać hotspot?" yes="Tak, zatrzymaj" no="Nie" danger
      onyes={() => power(false)} onno={() => (confirmStop = false)}>
      <ul>
        <li>Sieć „{hs.settings.ssid}” zniknie: wszystkie telefony ({view.relays.filter((r) => r.connected).length})
          i urządzenia ({hs.clients}) zostaną rozłączone.</li>
        <li>Hub nie będzie przesyłał danych, dopóki nie uruchomisz hotspotu ponownie.</li>
        <li>Po ponownym uruchomieniu telefony połączą się same (bez parowania). Po restarcie usługi
          xs-hubd hotspot startuje automatycznie.</li>
      </ul>
    </ConfirmDialog>
  {/if}

  <div class="card">
    <h2>Serwer</h2>
    {#if view.enrolled}
      <p>Zarejestrowany{#if view.server_ep}: <code>{view.server_ep}</code>{/if}</p>
      <p class="muted small">Tunel: {view.tunnel_up ? 'działa' : 'nie działa'}{view.tunnel_error ? ` (${view.tunnel_error})` : ''}</p>
    {:else}
      <p>Hub nie jest jeszcze zarejestrowany.</p>
    {/if}
    <form onsubmit={(e) => (e.preventDefault(), enroll())}>
      <label>
        Kod rejestracji (XS1…) od administratora serwera
        <textarea bind:value={code} rows="3" placeholder="XS1.…" spellcheck="false"></textarea>
      </label>
      <p class="muted small">{pathHint(view)}</p>
      {#if view.enrolled}
        <p class="muted small">Ponowna rejestracja zastępuje obecny serwer; usługa xs-hubd uruchomi się od nowa,
          a telefony połączą się same.</p>
      {/if}
      <button class="primary" disabled={busy || !code.trim().startsWith('XS1.')}>
        {busy ? 'Rejestruję…' : view.enrolled ? 'Zarejestruj ponownie' : 'Zarejestruj'}
      </button>
    </form>
    {#if enrolled}
      <div class="banner result">
        Zarejestrowano jako <b>{enrolled.name}</b>, adres w tunelu <code>{enrolled.tunnel_ip}</code>, MTU {enrolled.mtu}.
        <br />Serwer: {enrolled.endpoints?.join(', ')}
      </div>
    {/if}
  </div>

  <BypassCard {notify} />
  {#if windows}
    <section class="card"><h2>Przekierowanie portów</h2><p class="muted">Niedostępne w hubie na Windows (Mobile Hotspot nie pozwala dodać przekierowań).</p></section>
  {:else}
    <ForwardsCard {notify} />
  {/if}
  <NotifyCard {notify} />
  <CloseCard {notify} />
  <UpdateCard {notify} />
  <DiagCard {view} {notify} />
</div>

<style>
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
