<script>
  import Login from './components/Login.svelte'
  import Dashboard from './components/Dashboard.svelte'
  import Hubs from './components/Hubs.svelte'
  import HubDetail from './components/HubDetail.svelte'
  import Forwards from './components/Forwards.svelte'
  import Info from './components/Info.svelte'
  import Audit from './components/Audit.svelte'
  import Logs from './components/Logs.svelte'
  import Toast from './components/Toast.svelte'
  import RestartBanner from './components/RestartBanner.svelte'
  import { api, setCsrf, stream } from './lib/api.js'
  import LangSwitch from './components/LangSwitch.svelte'
  import { t } from './i18n/i18n.svelte.js'

  let user = $state(null) // null = checking, '' = logged out
  let ingress = $state(false) // behind Home Assistant ingress: HA does the login
  let route = $state(location.hash || '#/')
  let history = $state([])
  let sample = $state(null)
  let sessions = $state([])
  let hubs = $state([])
  let info = $state(null)
  let infoErr = $state('')
  let live = $state(false)
  let toastMsg = $state('')
  let toastBad = $state(false)
  let toastTimer
  let es = null

  const pages = [
    ['#/', 'nav.dashboard'],
    ['#/hubs', 'nav.hubs'],
    ['#/forwards', 'nav.forwards'],
    ['#/info', 'nav.server'],
    ['#/audit', 'nav.audit'],
    ['#/logs', 'nav.logs'],
  ]

  function toast(msg, bad = false) {
    toastMsg = msg
    toastBad = bad
    clearTimeout(toastTimer)
    toastTimer = setTimeout(() => (toastMsg = ''), 4000)
  }

  async function loadHubs() {
    try {
      hubs = (await api.hubs()) || []
    } catch (err) {
      if (err.status === 401) loggedOut()
    }
  }

  async function loadInfo() {
    try {
      info = await api.info()
      infoErr = ''
    } catch (err) {
      if (err.status === 401) loggedOut()
      else infoErr = err.message
    }
  }

  function start(s) {
    setCsrf(s.csrf)
    ingress = !!s.ingress
    user = s.user
    loadHubs()
    loadInfo()
    es?.close()
    es = stream({
      history: (h) => {
        history = h
        live = true
      },
      tick: (tk) => {
        live = true
        const prev = sample
        sample = tk.sample
        sessions = tk.sessions
        history = [...history.slice(-299), tk.sample]
        if (!prev || prev.total !== tk.sample.total || prev.online !== tk.sample.online) loadHubs()
      },
      logout: loggedOut,
      error: () => {
        live = false
        // A dead session makes the browser retry forever: check it.
        api.session().catch((err) => err.status === 401 && loggedOut())
      },
    })
  }

  function loggedOut() {
    es?.close()
    es = null
    setCsrf('')
    user = ''
    hubs = []
    sessions = []
    history = []
    sample = null
  }

  async function logout() {
    try {
      await api.logout()
    } catch {
      // logged out either way
    }
    loggedOut()
  }

  api.session().then(start, () => (user = ''))

  function hubName(enc) {
    try {
      return decodeURIComponent(enc)
    } catch {
      return enc // malformed %-escape typed into the address bar
    }
  }
  let detail = $derived(route.startsWith('#/hubs/') ? hubName(route.slice(7)) : null)
  let active = $derived(detail !== null ? '#/hubs' : route)
</script>

<svelte:window
  onhashchange={() => {
    route = location.hash || '#/'
    if (user) loadInfo()
  }}
/>

{#if user === ''}
  <Login onlogin={start} />
{:else if user}
  <header>
    <strong>CrossStream</strong>
    <nav>
      {#each pages as [href, label]}
        <a {href} class:active={active === href}>{t(label)}</a>
      {/each}
    </nav>
    <span class="who">
      <span class="dot" class:on={live} title={live ? t('app.live') : t('app.notLive')}></span>
      {user}
      <LangSwitch />
      {#if !ingress}<button onclick={logout}>{t('app.logout')}</button>{/if}
    </span>
  </header>
  <main>
    <RestartBanner {info} {toast} onchange={loadInfo} />
    {#if detail !== null}
      <HubDetail name={detail} {hubs} {sessions} />
    {:else if route === '#/hubs'}
      <Hubs {hubs} {sessions} onchange={loadHubs} {toast} />
    {:else if route === '#/forwards'}
      <Forwards {hubs} onchange={loadHubs} {toast} />
    {:else if route === '#/info'}
      <Info {info} error={infoErr} {hubs} {toast} onchange={loadInfo} />
    {:else if route === '#/audit'}
      <Audit />
    {:else if route === '#/logs'}
      <Logs />
    {:else}
      <Dashboard {history} {sample} />
    {/if}
  </main>
  <Toast message={toastMsg} bad={toastBad} />
{/if}

<style>
  header {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 8px 18px;
    padding: 10px 16px;
    background: var(--panel);
    border-bottom: 1px solid var(--line);
  }
  nav {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  nav a {
    color: var(--text);
    text-decoration: none;
    padding: 4px 10px;
    border-radius: 6px;
  }
  nav a.active {
    background: var(--chip);
    font-weight: 600;
  }
  .who {
    margin-left: auto;
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .dot {
    width: 9px;
    height: 9px;
    border-radius: 50%;
    background: var(--bad);
  }
  .dot.on {
    background: var(--ok);
  }
  main {
    max-width: 1200px;
    margin: 0 auto;
    padding: 16px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
</style>
