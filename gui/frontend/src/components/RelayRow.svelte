<script>
  import { api, errorText } from '../lib/api.js'
  import { mbps, megabytes, ms, netLabel, lastReason, ago } from '../lib/fmt.js'
  import ConfirmDialog from './ConfirmDialog.svelte'

  let { rv, notify } = $props()
  let asking = $state(false)

  const priorities = [
    ['normal', 'główny'],
    ['secondary', 'dodatkowy'],
    ['backup', 'zapasowy'],
  ]
  const capRank = $derived(Math.max(0, priorities.findIndex(([v]) => v === rv.max_priority)))
  const directions = [
    ['on', 'wł.'],
    ['backup_only', 'awaryjnie'],
    ['off', 'wył.'],
  ]
  const pathState = { alive: 'aktywny', suspect: 'niepewny', sleeping: 'uśpiony', dead: 'brak ścieżki' }

  async function set(params, what, el, shown) {
    try {
      await api.setRelay(rv.id, params)
      notify(`${rv.name}: ${what}`)
    } catch (e) {
      // The control is bound one way: put back what the hub still has.
      if (el && el.type === 'checkbox') el.checked = shown
      else if (el) el.value = shown ?? ''
      notify(errorText(e), true)
    }
  }

  function setNumber(e, key, what) {
    const v = e.currentTarget.value === '' ? 0 : Number(e.currentTarget.value)
    if (!Number.isInteger(v) || v < 0 || v > 4294967295) {
      notify('Podaj liczbę całkowitą ≥ 0', true)
      return
    }
    set({ [key]: v }, v ? `${what} ${v}` : `${what} wyłączony`, e.currentTarget, rv[key] || '')
  }

  async function forget() {
    asking = false
    try {
      await api.forget(rv.id)
      notify(`${rv.name} usunięty — aby wrócił, sparuj go ponownie`)
    } catch (e) {
      notify(errorText(e), true)
    }
  }
</script>

<tr class:away={!rv.connected}>
  <td>
    <div class="name">{rv.name}</div>
    <div class="muted small">
      {rv.carrier || '—'}
      {#if rv.net_type}· {netLabel[rv.net_type] ?? rv.net_type}{/if}
      {#if rv.signal_dbm}· {rv.signal_dbm} dBm{/if}
      {#if rv.battery >= 0}· 🔋 {rv.battery}%{/if}
    </div>
  </td>
  <td class="state">
    {#if rv.connected}
      <span class="dot ok"></span>{pathState[rv.path_state] ?? 'połączony'}
      {#if rv.direct_flows}<span class="muted small"> · {rv.direct_flows} poł. bezpośr.</span>{/if}
      {#if rv.capped}<div class="chip bad">Limit danych osiągnięty</div>{/if}
      {#if !rv.cellular_ok}
        <div class="chip bad" title="Telefon zgłasza, że jego dane komórkowe nie działają">Brak danych komórkowych</div>
      {/if}
      {#if rv.weak_wifi}
        <div class="chip warn" title="Wi-Fi telefon ↔ hub: {rv.wifi_rssi} dBm. Przybliż telefon do laptopa.">
          Słaby Wi-Fi: bliżej huba
        </div>
      {/if}
      {#if rv.weak_cell}
        <div class="chip warn" title="Słaby sygnał lub duże straty. Poproś o przeniesienie telefonu (okno, wyżej).">
          Słaby zasięg: przenieś tel.
        </div>
      {/if}
    {:else}
      <span class="dot"></span>rozłączony
      {#if rv.last_reason}
        <div class="muted small">{lastReason(rv.last_reason)}, {ago(rv.last_seen)}</div>
      {/if}
    {/if}
  </td>
  <td>
    {#if rv.connected && rv.transport}
      <span class:chip={rv.transport !== 'udp'} class:warn={rv.transport !== 'udp'}
        title={rv.transport === 'udp' ? 'UDP' : 'Operator blokuje UDP: tunel idzie przez ' + rv.transport.toUpperCase() + ' (wolniej)'}>
        {rv.transport.toUpperCase()}
      </span>
    {:else}—{/if}
  </td>
  <td class="num">
    {#if rv.connected}
      <span class="up">{mbps(rv.up_mbps)}</span><br /><span class="down">{mbps(rv.down_mbps)}</span>
    {:else}—{/if}
  </td>
  <td class="num">
    {#if rv.has_path}
      {ms(rv.rtt_ms)} · {ms(rv.jitter_ms)}<br />
      <span class:lossy={rv.loss_pct > 5}>{rv.loss_pct.toFixed(1)}%</span>
    {:else}—{/if}
  </td>
  <td class="num">
    {megabytes(rv.used_mb)}
    {#if rv.cap_mb}<div class="muted small">limit: {megabytes(rv.cap_used_mb)} / {megabytes(rv.cap_mb)}</div>{/if}
  </td>
  <td>
    <select value={rv.priority} onchange={(e) => set({ priority: e.currentTarget.value }, 'priorytet zmieniony', e.currentTarget, rv.priority)}>
      {#each priorities as [v, label], i}<option value={v} disabled={i < capRank}>{label}</option>{/each}
    </select>
    {#if capRank > 0}
      <span class="lock" title={`Właściciel telefonu pozwala najwyżej na: ${priorities[capRank][1]}`}>🔒</span>
    {/if}
  </td>
  <td>
    <select value={rv.up} onchange={(e) => set({ up: e.currentTarget.value }, 'wysyłanie zmienione', e.currentTarget, rv.up)}>
      {#each directions as [v, label]}<option value={v}>{label}</option>{/each}
    </select>
  </td>
  <td>
    <select value={rv.down} onchange={(e) => set({ down: e.currentTarget.value }, 'pobieranie zmienione', e.currentTarget, rv.down)}>
      {#each directions as [v, label]}<option value={v}>{label}</option>{/each}
    </select>
  </td>
  <td>
    <input type="number" min="0" placeholder="bez" value={rv.rate_kbps || ''}
      onchange={(e) => setNumber(e, 'rate_kbps', 'limit prędkości kb/s')} />
  </td>
  <td>
    <input type="number" min="0" placeholder="bez" value={rv.cap_mb || ''}
      onchange={(e) => setNumber(e, 'cap_mb', 'limit danych MB')} />
  </td>
  <td>
    <button class="danger" onclick={() => (asking = true)}>Usuń</button>
    {#if asking}
      <ConfirmDialog title="Usunąć telefon „{rv.name}”?" yes="Tak, usuń" no="Nie" danger
        onyes={forget} onno={() => (asking = false)}>
        <p>
          {rv.carrier || 'Operator nieznany'} · {rv.connected ? 'połączony teraz' : 'rozłączony'}
          · zużyte w tej sesji: {megabytes(rv.used_mb)}
        </p>
        <ul>
          <li>Hub rozłączy telefon i zapomni go — przestanie przez niego przesyłać dane.</li>
          <li>Ustawienia telefonu (priorytet, kierunki, limity) zostaną usunięte.</li>
          <li>Żeby znów go używać, trzeba zeskanować nowy kod QR w aplikacji CrossStream Relay.</li>
        </ul>
      </ConfirmDialog>
    {/if}
  </td>
</tr>

<style>
  .lock {
    margin-left: 4px;
    cursor: help;
  }
  td {
    padding: 7px 5px;
    height: 76px; /* room for the status line + two hints: rows do not jump */
    border-bottom: 1px solid var(--line);
    vertical-align: top;
  }
  td.num {
    text-align: right;
    white-space: nowrap;
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
  td.num {
    font-variant-numeric: tabular-nums;
    min-width: 80px;
  }
  ul {
    padding-left: 18px;
    margin: 8px 0 0;
  }
  li {
    margin: 4px 0;
  }
  p {
    margin: 0;
    color: var(--muted);
  }
  .name {
    min-width: 150px;
  }
  select {
    width: 108px;
  }
  tr.away td {
    opacity: 0.65;
  }
  .name {
    font-weight: 600;
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
  input {
    width: 64px;
  }
</style>
