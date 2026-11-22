<script>
  import { api, errorText } from '../lib/api.js'
  import { mbps, megabytes, ms } from '../lib/fmt.js'
  import ConfirmDialog from './ConfirmDialog.svelte'

  let { u, notify } = $props()
  let asking = $state(false)
  let editing = $state(null) // limits dialog form
  let renaming = $state(false)
  let loggingIn = $state(false)

  // Login page of a hotel/train network: route it through this link and
  // open it in the browser (or show what to do instead).
  async function login() {
    loggingIn = true
    try {
      const msg = await api.captiveLogin(u.id)
      notify(msg || `${u.name}: strona logowania otwarta w przeglądarce`, !!msg)
    } catch (e) {
      notify(errorText(e), true)
    } finally {
      loggingIn = false
    }
  }

  const kinds = {
    usb: ['🔌', 'Telefon przez USB'],
    bluetooth: ['ᛒ', 'Bluetooth (wolne, tylko zapas)'],
    modem: ['📶', 'Modem / karta SIM'],
    ethernet: ['🖧', 'Ethernet'],
    wifi: ['📡', 'Wi-Fi (druga karta)'],
  }
  const priorities = [
    ['normal', 'główny'],
    ['secondary', 'dodatkowy'],
    ['backup', 'zapasowy'],
  ]
  const directions = [
    ['on', 'wł.'],
    ['backup_only', 'awaryjnie'],
    ['off', 'wył.'],
  ]
  const pathState = { alive: 'aktywne', suspect: 'niepewne', sleeping: 'uśpione', dead: 'brak ścieżki' }
  const periods = [
    ['session', 'od uruchomienia huba'],
    ['day', 'na dzień'],
    ['month', 'na miesiąc'],
  ]

  async function set(params, what, el, shown) {
    try {
      await api.setUplink({ uplink: u.id, ...params })
      notify(`${u.name}: ${what}`)
      return true
    } catch (e) {
      // The control is bound one way: put back what the hub still has.
      if (el && el.type === 'checkbox') el.checked = shown
      else if (el) el.value = shown ?? ''
      notify(errorText(e), true)
      return false
    }
  }

  function setRate(e) {
    const v = e.currentTarget.value === '' ? 0 : Number(e.currentTarget.value)
    if (!Number.isInteger(v) || v < 0 || v > 4294967295) {
      notify('Podaj liczbę całkowitą ≥ 0', true)
      return
    }
    set({ rate_kbps: v }, v ? `limit prędkości ${v} kb/s` : 'limit prędkości wyłączony', e.currentTarget, u.rate_kbps || '')
  }

  function rename(e) {
    renaming = false
    const name = e.currentTarget.value.trim()
    if (name && name !== u.name) set({ name }, 'nazwa zmieniona')
  }

  function openLimits() {
    const l = u.limits
    editing = {
      cap: l.cap_mb ? String(l.cap_mb) : '',
      period: l.cap_period,
      resetDay: String(l.reset_day || 1),
      throttle: l.over_cap === 'throttle',
      ttl: u.ttl,
    }
  }

  async function saveLimits() {
    const cap = editing.cap === '' ? 0 : Number(editing.cap)
    const day = Number(editing.resetDay)
    if (!Number.isInteger(cap) || cap < 0) {
      notify('Limit danych: liczba całkowita MB ≥ 0', true)
      return
    }
    if (!Number.isInteger(day) || day < 1 || day > 28) {
      notify('Dzień odnowienia: 1–28', true)
      return
    }
    const limits = {
      cap_mb: cap,
      cap_period: editing.period,
      reset_day: editing.period === 'month' ? day : 0,
      over_cap: editing.throttle ? 'throttle' : 'stop',
      throttle_kbps: editing.throttle ? 256 : 0,
    }
    if (await set({ limits, ttl: editing.ttl }, cap ? `limit ${cap} MB zapisany` : 'bez limitu danych')) editing = null
  }

  async function resetUsage() {
    if (await set({ reset_usage: true }, 'licznik wyzerowany')) editing = null
  }

  async function forget() {
    asking = false
    try {
      await api.forgetUplink(u.id)
      notify(`${u.name} usunięte z listy`)
    } catch (e) {
      notify(errorText(e), true)
    }
  }

  const capText = $derived(
    u.limits.cap_mb
      ? `${megabytes(u.used_mb)} / ${megabytes(u.limits.cap_mb)} ${
          { session: 'od startu', day: 'dziś', month: 'w miesiącu' }[u.limits.cap_period] ?? ''
        }`
      : '',
  )
</script>

<tr class:away={u.state !== 'up'}>
  <td>
    <label class="switch" title={u.enabled ? 'Wyłącz to łącze' : 'Włącz to łącze'}>
      <input type="checkbox" checked={u.enabled}
        onchange={(e) => set({ enabled: e.currentTarget.checked }, e.currentTarget.checked ? 'włączone' : 'wyłączone', e.currentTarget, u.enabled)} />
      <span>{u.enabled ? 'wł.' : 'wył.'}</span>
    </label>
  </td>
  <td>
    <div class="name">
      <span class="icon" title={kinds[u.kind]?.[1] ?? u.kind}>{kinds[u.kind]?.[0] ?? '?'}</span>
      {#if renaming}
        <!-- svelte-ignore a11y_autofocus -->
        <input class="rename" value={u.name} autofocus onblur={rename} onkeydown={(e) => e.key === 'Enter' && e.currentTarget.blur()} />
      {:else}
        <button class="link" title="Zmień nazwę" onclick={() => (renaming = true)}>{u.name}</button>
      {/if}
    </div>
    <div class="muted small">
      {kinds[u.kind]?.[1] ?? u.kind} · {u.iface}
      {#if u.tech}· {u.tech}{/if}
      {#if u.operator}· {u.operator}{/if}
      {#if u.signal_dbm}· {u.signal_dbm} dBm{:else if u.quality}· sygnał {u.quality}%{/if}
    </div>
  </td>
  <td class="state">
    {#if u.state === 'up'}
      <span class="dot ok"></span>{pathState[u.path_state] ?? 'połączone'}
      {#if u.transport && u.transport !== 'udp'}<span class="chip warn">{u.transport.toUpperCase()}</span>{/if}
      {#if u.direct_flows}<span class="muted small"> · {u.direct_flows} poł. bezpośr.</span>{/if}
    {:else if u.state === 'connecting'}
      <span class="dot"></span>łączenie z serwerem…
    {:else if u.state === 'limit'}
      <span class="dot bad"></span>wyłączone
      <div class="chip bad" title="Łącze wróci w nowym okresie albo po wyzerowaniu licznika (Limit…)">
        Limit danych osiągnięty
      </div>
    {:else if u.state === 'captive'}
      <span class="dot bad"></span>Wymaga logowania
      <div>
        <button class="small" onclick={login} disabled={loggingIn}
          title="Otwiera stronę logowania tej sieci (hotel, pociąg) w przeglądarce">
          {loggingIn ? 'Sprawdzam…' : 'Zaloguj'}
        </button>
      </div>
    {:else if u.state === 'absent'}
      <span class="dot"></span>odłączone
    {:else}
      <span class="dot"></span>wyłączone
    {/if}
    {#if u.same_ip_as}
      <div class="chip warn" title="To łącze ma ten sam publiczny adres IP co „{u.same_ip_as}” — to samo połączenie, bez zysku.">
        {u.kind === 'usb' ? 'Wyłącz Wi-Fi w telefonie' : 'To samo łącze co ' + u.same_ip_as}
      </div>
    {/if}
    {#if u.no_internet && u.state !== 'captive'}
      <div class="chip warn" title="Sprawdzenie strony logowania nie dostało odpowiedzi przez to łącze">Brak internetu</div>
    {/if}
    {#if u.weak_cell}
      <div class="chip warn" title="Słaby sygnał modemu lub duże straty. Przenieś laptopa bliżej okna.">Słaby zasięg modemu</div>
    {/if}
  </td>
  <td class="num">
    {#if u.state === 'up'}
      <span class="up">{mbps(u.up_mbps)}</span><br /><span class="down">{mbps(u.down_mbps)}</span>
    {:else}—{/if}
  </td>
  <td class="num">
    {#if u.has_path}
      {ms(u.rtt_ms)}<br /><span class:lossy={u.loss_pct > 5}>{u.loss_pct.toFixed(1)}%</span>
    {:else}—{/if}
  </td>
  <td class="num">
    {megabytes(u.used_mb)}
    {#if capText}<div class="muted small">limit: {capText}</div>{/if}
  </td>
  <td>
    <select value={u.priority} onchange={(e) => set({ priority: e.currentTarget.value }, 'priorytet zmieniony', e.currentTarget, u.priority)}>
      {#each priorities as [v, label]}<option value={v}>{label}</option>{/each}
    </select>
  </td>
  <td>
    <select value={u.up} onchange={(e) => set({ up: e.currentTarget.value }, 'wysyłanie zmienione', e.currentTarget, u.up)}>
      {#each directions as [v, label]}<option value={v}>{label}</option>{/each}
    </select>
  </td>
  <td>
    <select value={u.down} onchange={(e) => set({ down: e.currentTarget.value }, 'pobieranie zmienione', e.currentTarget, u.down)}>
      {#each directions as [v, label]}<option value={v}>{label}</option>{/each}
    </select>
  </td>
  <td>
    <input type="number" min="0" placeholder="bez" value={u.rate_kbps || ''} onchange={setRate} />
  </td>
  <td class="actions">
    <button onclick={openLimits}>Limit…</button>
    <button class="danger" onclick={() => (asking = true)}>Usuń</button>
    {#if editing}
      <ConfirmDialog title="Limity łącza „{u.name}”" yes="Zapisz" no="Anuluj" onyes={saveLimits} onno={() => (editing = null)}>
        <div class="form">
          <label>Limit danych <input type="number" min="0" placeholder="bez" bind:value={editing.cap} /> MB</label>
          <label>
            Okres
            <select bind:value={editing.period}>
              {#each periods as [v, label]}<option value={v}>{label}</option>{/each}
            </select>
          </label>
          {#if editing.period === 'month'}
            <label>Dzień odnowienia <input type="number" min="1" max="28" bind:value={editing.resetDay} /></label>
          {/if}
          <label class="check">
            <input type="checkbox" bind:checked={editing.throttle} />
            Po przekroczeniu zwolnij do 256 kb/s zamiast wyłączać
          </label>
          <label class="check" title="Operatorzy wykrywają udostępnianie internetu po TTL; 65 ukrywa laptopa za telefonem">
            <input type="checkbox" bind:checked={editing.ttl} />
            Ustaw TTL 65 (ukryj udostępnianie przed operatorem)
          </label>
          <p class="muted small">
            Zużyte w tym okresie: {megabytes(u.used_mb)}
            <button class="link" onclick={resetUsage}>Wyzeruj licznik</button>
          </p>
        </div>
      </ConfirmDialog>
    {/if}
    {#if asking}
      <ConfirmDialog title="Usunąć łącze „{u.name}” z listy?" yes="Tak, usuń" no="Nie" danger onyes={forget}
        onno={() => (asking = false)}>
        <p>Hub przestanie go używać i zapomni jego ustawienia oraz licznik. Jeśli łącze jest podłączone,
          pojawi się znowu jako nowe, wyłączone.</p>
      </ConfirmDialog>
    {/if}
  </td>
</tr>

<style>
  td {
    padding: 7px 5px;
    height: 60px;
    border-bottom: 1px solid var(--line);
    vertical-align: top;
  }
  td.num {
    text-align: right;
    white-space: nowrap;
    font-variant-numeric: tabular-nums;
    min-width: 80px;
  }
  td.state {
    width: 170px;
    min-width: 170px;
  }
  td.state .chip {
    display: block;
    width: fit-content;
    margin-top: 3px;
  }
  td.actions {
    white-space: nowrap;
  }
  tr.away td {
    opacity: 0.75;
  }
  .name {
    font-weight: 600;
    min-width: 160px;
    display: flex;
    gap: 6px;
    align-items: center;
  }
  .icon {
    width: 20px;
    text-align: center;
    cursor: help;
  }
  .link {
    background: none;
    border: none;
    padding: 0;
    font: inherit;
    font-weight: inherit;
    color: inherit;
    cursor: pointer;
    text-decoration: underline dotted;
  }
  .rename {
    width: 150px;
  }
  .switch {
    display: flex;
    gap: 4px;
    align-items: center;
  }
  .dot {
    display: inline-block;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--muted);
    margin-right: 6px;
  }
  .dot.ok {
    background: var(--ok);
  }
  .dot.bad {
    background: var(--bad);
  }
  .up {
    color: var(--up);
  }
  .down {
    color: var(--down);
  }
  .lossy {
    color: var(--bad);
    font-weight: 600;
  }
  select {
    width: 108px;
  }
  input[type='number'] {
    width: 64px;
  }
  .form {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .form label {
    display: flex;
    gap: 8px;
    align-items: center;
  }
  .form select {
    width: auto;
  }
  .form input[type='number'] {
    width: 90px;
  }
  .form p {
    margin: 0;
  }
</style>
