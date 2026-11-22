<script>
  import { onMount } from 'svelte'
  import { api, onView, onCloseAsk } from './lib/api.js'
  import { modeLabel, mbps } from './lib/fmt.js'
  import ErrorState from './components/ErrorState.svelte'
  import Chart from './components/Chart.svelte'
  import Pairing from './components/Pairing.svelte'
  import Relays from './components/Relays.svelte'
  import Bonding from './components/Bonding.svelte'
  import Settings from './components/Settings.svelte'
  import Thanks from './components/Thanks.svelte'
  import Toast from './components/Toast.svelte'
  import Uplinks from './components/Uplinks.svelte'
  import CloseDialog from './components/CloseDialog.svelte'
  import EnrollDialog from './components/EnrollDialog.svelte'
  import UpdateBanner from './components/UpdateBanner.svelte'
  import UpdateConfirm from './components/UpdateConfirm.svelte'
  import { upd, startUpdates } from './lib/update.svelte.js'

  let view = $state({ state: 'daemon', relays: [], uplinks: [], history: [] })
  let tab = $state('main')
  let toast = $state(null)
  let closing = $state(false) // close dialog (window close while xs-hubd runs)
  let enrollAsked = $state(false) // the start-up enrollment dialog was shown
  let enrolling = $state(false)
  $effect(() => {
    if (!enrollAsked && view.state === 'ok' && view.enrolled === false) {
      enrollAsked = true
      enrolling = true
    }
  })

  onMount(() => {
    api.current().then((v) => (view = v)).catch(() => {})
    const offView = onView((v) => (view = v))
    const offClose = onCloseAsk(() => (closing = true))
    const offUpdate = startUpdates()
    return () => {
      offView()
      offClose()
      offUpdate()
    }
  })

  // notify shows an action result or error for a few seconds.
  function notify(text, bad = false) {
    toast = { text, bad, id: Date.now() }
  }

  const modeClass = { BONDED: 'ok', DIRECT: 'warn', CONNECTING: 'warn', OFFLINE: 'bad' }
</script>

<header>
  <div class="brand">
    <strong>CrossStream Hub</strong>
    {#if view.hub}<span class="muted">· {view.hub}</span>{/if}
  </div>
  {#if view.state === 'ok'}
    <span class="badge {modeClass[view.mode] ?? 'bad'}">{modeLabel[view.mode] ?? view.mode}</span>
    <span class="stat" title="Urządzenia połączone z hotspotem">Klienci: <b>{view.clients}</b></span>
    <span class="stat"><span class="up">↑ {mbps(view.up_mbps)}</span> <span class="down">↓ {mbps(view.down_mbps)}</span></span>
  {/if}
  {#if view.state === 'ok'}
    <nav>
      <button class:active={tab === 'main'} onclick={() => (tab = 'main')}>Panel</button>
      <button class:active={tab === 'thanks'} onclick={() => (tab = 'thanks')}>Statystyki</button>
      <button class:active={tab === 'settings'} onclick={() => (tab = 'settings')}>Ustawienia</button>
    </nav>
  {/if}
</header>

<main>
  <UpdateBanner {notify} />
  {#if view.state !== 'ok'}
    <ErrorState {view} />
  {:else}
    {#if view.mode === 'DIRECT'}
      <div class="banner warn">
        <b>Tryb bezpośredni — brak tunelu do serwera.</b>
        Ruch wychodzi z adresów IP telefonów. Telefony i ich operatorzy widzą, z jakimi adresami się łączysz,
        a strony mogą widzieć różne adresy IP w jednej sesji. Używaj tylko szyfrowanych połączeń (HTTPS).
        {#if view.direct?.draining}<br />Kończenie starych połączeń po powrocie tunelu…{/if}
      </div>
    {/if}
    {#if !view.enrolled}
      <div class="banner warn">
        Hub nie jest zarejestrowany na serwerze.
        <button onclick={() => (enrolling = true)}>Zarejestruj</button>
      </div>
    {:else if view.tunnel_error}
      <div class="banner bad">Tunel: {view.tunnel_error}</div>
    {/if}
    {#each view.warnings ?? [] as w}
      <div class="banner warn">{w}</div>
    {/each}

    {#if tab === 'main'}
      <div class="top">
        <Chart history={view.history} />
        <Pairing {notify} />
      </div>
      <Relays {view} {notify} />
      <Uplinks {view} {notify} />
      <Bonding {view} {notify} />
    {:else if tab === 'thanks'}
      <Thanks {notify} />
    {:else}
      <Settings {view} {notify} />
    {/if}
  {/if}
</main>

<Toast {toast} />
{#if closing}<CloseDialog {notify} onclose={() => (closing = false)} />{/if}
{#if upd.confirm}<UpdateConfirm {notify} />{/if}
{#if enrolling && !view.enrolled}<EnrollDialog {view} {notify} onclose={() => (enrolling = false)} />{/if}

<style>
  header {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 10px 18px;
    background: var(--panel);
    border-bottom: 1px solid var(--line);
    position: sticky;
    top: 0;
    z-index: 2;
  }
  .brand {
    font-size: 16px;
  }
  nav {
    margin-left: auto;
    display: flex;
    gap: 6px;
  }
  nav button.active {
    background: var(--accent);
    border-color: var(--accent);
    color: #fff;
  }
  .badge {
    border-radius: 12px;
    padding: 2px 10px;
    font-weight: 600;
    font-size: 13px;
  }
  .badge.ok {
    background: color-mix(in srgb, var(--ok) 18%, transparent);
    color: var(--ok);
  }
  .badge.warn {
    background: var(--warn-bg);
    color: var(--warn);
  }
  .badge.bad {
    background: var(--bad-bg);
    color: var(--bad);
  }
  .stat {
    color: var(--muted);
  }
  .up {
    color: var(--up);
    font-weight: 600;
  }
  .down {
    color: var(--down);
    font-weight: 600;
  }
  main {
    padding: 16px 18px 40px;
    max-width: 1500px;
    margin: 0 auto;
  }
  .top {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 280px;
    align-items: start;
    gap: 14px;
    margin-bottom: 14px;
  }
</style>
