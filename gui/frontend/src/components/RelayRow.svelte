<script>
  import { api, errorText } from '../lib/api.js'
  import { mbps, megabytes, ms, netLabel, lastReason, ago } from '../lib/fmt.js'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import { t, num } from '../i18n/i18n.svelte.js'

  let { rv, notify } = $props()
  let asking = $state(false)

  const priorities = ['normal', 'secondary', 'backup']
  const capRank = $derived(Math.max(0, priorities.indexOf(rv.max_priority)))
  const directions = ['on', 'backup_only', 'off']
  const pathStates = ['alive', 'suspect', 'sleeping', 'dead']

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
      notify(t('relay.intError'), true)
      return
    }
    set({ [key]: v }, v ? `${what} ${v}` : t('relay.limitOff', { what }), e.currentTarget, rv[key] || '')
  }

  async function forget() {
    asking = false
    try {
      await api.forget(rv.id)
      notify(t('relay.forgotten', { name: rv.name }))
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
      <span class="dot ok"></span>{pathStates.includes(rv.path_state) ? t('relay.path.' + rv.path_state) : t('relay.connected')}
      {#if rv.direct_flows}<span class="muted small"> · {t('relay.directFlows', { n: rv.direct_flows })}</span>{/if}
      {#if rv.capped}<div class="chip bad">{t('relay.capped')}</div>{/if}
      {#if !rv.cellular_ok}
        <div class="chip bad" title={t('relay.noCellHint')}>{t('relay.noCell')}</div>
      {/if}
      {#if rv.weak_wifi}
        <div class="chip warn" title={t('relay.weakWifiHint', { dbm: rv.wifi_rssi })}>
          {t('relay.weakWifi')}
        </div>
      {/if}
      {#if rv.weak_cell}
        <div class="chip warn" title={t('relay.weakCellHint')}>
          {t('relay.weakCell')}
        </div>
      {/if}
    {:else}
      <span class="dot"></span>{t('relay.disconnected')}
      {#if rv.last_reason}
        <div class="muted small">{lastReason(rv.last_reason)}, {ago(rv.last_seen)}</div>
      {/if}
    {/if}
  </td>
  <td>
    {#if rv.connected && rv.transport}
      <span class:chip={rv.transport !== 'udp'} class:warn={rv.transport !== 'udp'}
        title={rv.transport === 'udp' ? 'UDP' : t('relay.udpBlocked', { via: rv.transport.toUpperCase() })}>
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
      <span class:lossy={rv.loss_pct > 5}>{num(rv.loss_pct, 1)}%</span>
    {:else}—{/if}
  </td>
  <td class="num">
    {megabytes(rv.used_mb)}
    {#if rv.cap_mb}<div class="muted small">{t('relay.capUsed', { used: megabytes(rv.cap_used_mb), cap: megabytes(rv.cap_mb) })}</div>{/if}
  </td>
  <td>
    <select value={rv.priority} onchange={(e) => set({ priority: e.currentTarget.value }, t('relay.prioChanged'), e.currentTarget, rv.priority)}>
      {#each priorities as v, i}<option value={v} disabled={i < capRank}>{t('prio.' + v)}</option>{/each}
    </select>
    {#if capRank > 0}
      <span class="lock" title={t('relay.prioCap', { prio: t('prio.' + priorities[capRank]) })}>🔒</span>
    {/if}
  </td>
  <td>
    <select value={rv.up} onchange={(e) => set({ up: e.currentTarget.value }, t('relay.upChanged'), e.currentTarget, rv.up)}>
      {#each directions as v}<option value={v}>{t('dir.' + v)}</option>{/each}
    </select>
  </td>
  <td>
    <select value={rv.down} onchange={(e) => set({ down: e.currentTarget.value }, t('relay.downChanged'), e.currentTarget, rv.down)}>
      {#each directions as v}<option value={v}>{t('dir.' + v)}</option>{/each}
    </select>
  </td>
  <td>
    <input type="number" min="0" placeholder={t('relay.none')} value={rv.rate_kbps || ''}
      onchange={(e) => setNumber(e, 'rate_kbps', t('relay.rateLimit'))} />
  </td>
  <td>
    <input type="number" min="0" placeholder={t('relay.none')} value={rv.cap_mb || ''}
      onchange={(e) => setNumber(e, 'cap_mb', t('relay.dataLimit'))} />
  </td>
  <td>
    <button class="danger" onclick={() => (asking = true)}>{t('common.delete')}</button>
    {#if asking}
      <ConfirmDialog title={t('relay.forgetAsk', { name: rv.name })} yes={t('common.deleteYes')} no={t('common.no')} danger
        onyes={forget} onno={() => (asking = false)}>
        <p>
          {rv.carrier || t('relay.carrierUnknown')} · {rv.connected ? t('relay.connectedNow') : t('relay.disconnected')}
          · {t('relay.usedSession', { mb: megabytes(rv.used_mb) })}
        </p>
        <ul>
          <li>{t('relay.forget1')}</li>
          <li>{t('relay.forget2')}</li>
          <li>{t('relay.forget3')}</li>
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
