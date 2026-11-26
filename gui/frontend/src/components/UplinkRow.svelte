<script>
  import { api, errorText } from '../lib/api.js'
  import { mbps, megabytes, ms } from '../lib/fmt.js'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import { t, num } from '../i18n/i18n.svelte.js'

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
      notify(msg || t('uplink.loginOpened', { name: u.name }), !!msg)
    } catch (e) {
      notify(errorText(e), true)
    } finally {
      loggingIn = false
    }
  }

  const icons = { usb: '🔌', bluetooth: 'ᛒ', modem: '📶', ethernet: '🖧', wifi: '📡' }
  const kindName = (k) => (k in icons ? t('uplink.kind.' + k) : k)
  const priorities = ['normal', 'secondary', 'backup']
  const directions = ['on', 'backup_only', 'off']
  const pathStates = ['alive', 'suspect', 'sleeping', 'dead']
  const periods = ['session', 'day', 'month']

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
      notify(t('relay.intError'), true)
      return
    }
    set({ rate_kbps: v }, v ? t('uplink.rateSet', { n: v }) : t('uplink.rateOff'), e.currentTarget, u.rate_kbps || '')
  }

  function rename(e) {
    renaming = false
    const name = e.currentTarget.value.trim()
    if (name && name !== u.name) set({ name }, t('uplink.renamed'))
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
      notify(t('uplink.capError'), true)
      return
    }
    if (!Number.isInteger(day) || day < 1 || day > 28) {
      notify(t('uplink.dayError'), true)
      return
    }
    const limits = {
      cap_mb: cap,
      cap_period: editing.period,
      reset_day: editing.period === 'month' ? day : 0,
      over_cap: editing.throttle ? 'throttle' : 'stop',
      throttle_kbps: editing.throttle ? 256 : 0,
    }
    if (await set({ limits, ttl: editing.ttl }, cap ? t('uplink.capSaved', { n: cap }) : t('uplink.capNone'))) editing = null
  }

  async function resetUsage() {
    if (await set({ reset_usage: true }, t('uplink.resetDone'))) editing = null
  }

  async function forget() {
    asking = false
    try {
      await api.forgetUplink(u.id)
      notify(t('uplink.forgotten', { name: u.name }))
    } catch (e) {
      notify(errorText(e), true)
    }
  }

  const capText = $derived(
    u.limits.cap_mb
      ? `${megabytes(u.used_mb)} / ${megabytes(u.limits.cap_mb)} ${
          periods.includes(u.limits.cap_period) ? t('uplink.capWhen.' + u.limits.cap_period) : ''
        }`
      : '',
  )
</script>

<tr class:away={u.state !== 'up'}>
  <td>
    <label class="switch" title={u.enabled ? t('uplink.turnOff') : t('uplink.turnOn')}>
      <input type="checkbox" checked={u.enabled}
        onchange={(e) => set({ enabled: e.currentTarget.checked }, e.currentTarget.checked ? t('uplink.enabled') : t('uplink.disabled'), e.currentTarget, u.enabled)} />
      <span>{u.enabled ? t('dir.on') : t('dir.off')}</span>
    </label>
  </td>
  <td>
    <div class="name">
      <span class="icon" title={kindName(u.kind)}>{icons[u.kind] ?? '?'}</span>
      {#if renaming}
        <!-- svelte-ignore a11y_autofocus -->
        <input class="rename" value={u.name} autofocus onblur={rename} onkeydown={(e) => e.key === 'Enter' && e.currentTarget.blur()} />
      {:else}
        <button class="link" title={t('uplink.rename')} onclick={() => (renaming = true)}>{u.name}</button>
      {/if}
    </div>
    <div class="muted small">
      {kindName(u.kind)} · {u.iface}
      {#if u.tech}· {u.tech}{/if}
      {#if u.operator}· {u.operator}{/if}
      {#if u.signal_dbm}· {u.signal_dbm} dBm{:else if u.quality}· {t('uplink.quality', { n: u.quality })}{/if}
    </div>
  </td>
  <td class="state">
    {#if u.state === 'up'}
      <span class="dot ok"></span>{pathStates.includes(u.path_state) ? t('uplink.path.' + u.path_state) : t('uplink.connected')}
      {#if u.transport && u.transport !== 'udp'}<span class="chip warn">{u.transport.toUpperCase()}</span>{/if}
      {#if u.direct_flows}<span class="muted small"> · {t('relay.directFlows', { n: u.direct_flows })}</span>{/if}
    {:else if u.state === 'connecting'}
      <span class="dot"></span>{t('uplink.connecting')}
    {:else if u.state === 'limit'}
      <span class="dot bad"></span>{t('uplink.disabled')}
      <div class="chip bad" title={t('uplink.cappedHint')}>
        {t('relay.capped')}
      </div>
    {:else if u.state === 'captive'}
      <span class="dot bad"></span>{t('uplink.captive')}
      <div>
        <button class="small" onclick={login} disabled={loggingIn}
          title={t('uplink.loginHint')}>
          {loggingIn ? t('uplink.checking') : t('uplink.login')}
        </button>
      </div>
    {:else if u.state === 'absent'}
      <span class="dot"></span>{t('uplink.absent')}
    {:else}
      <span class="dot"></span>{t('uplink.disabled')}
    {/if}
    {#if u.same_ip_as}
      <div class="chip warn" title={t('uplink.sameIpHint', { name: u.same_ip_as })}>
        {u.kind === 'usb' ? t('uplink.sameIpUsb') : t('uplink.sameIp', { name: u.same_ip_as })}
      </div>
    {/if}
    {#if u.no_internet && u.state !== 'captive'}
      <div class="chip warn" title={t('uplink.noInternetHint')}>{t('uplink.noInternet')}</div>
    {/if}
    {#if u.weak_cell}
      <div class="chip warn" title={t('uplink.weakCellHint')}>{t('uplink.weakCell')}</div>
    {/if}
  </td>
  <td class="num">
    {#if u.state === 'up'}
      <span class="up">{mbps(u.up_mbps)}</span><br /><span class="down">{mbps(u.down_mbps)}</span>
    {:else}—{/if}
  </td>
  <td class="num">
    {#if u.has_path}
      {ms(u.rtt_ms)}<br /><span class:lossy={u.loss_pct > 5}>{num(u.loss_pct, 1)}%</span>
    {:else}—{/if}
  </td>
  <td class="num">
    {megabytes(u.used_mb)}
    {#if capText}<div class="muted small">{t('uplink.capText', { text: capText })}</div>{/if}
  </td>
  <td>
    <select value={u.priority} onchange={(e) => set({ priority: e.currentTarget.value }, t('relay.prioChanged'), e.currentTarget, u.priority)}>
      {#each priorities as v}<option value={v}>{t('prio.' + v)}</option>{/each}
    </select>
  </td>
  <td>
    <select value={u.up} onchange={(e) => set({ up: e.currentTarget.value }, t('relay.upChanged'), e.currentTarget, u.up)}>
      {#each directions as v}<option value={v}>{t('dir.' + v)}</option>{/each}
    </select>
  </td>
  <td>
    <select value={u.down} onchange={(e) => set({ down: e.currentTarget.value }, t('relay.downChanged'), e.currentTarget, u.down)}>
      {#each directions as v}<option value={v}>{t('dir.' + v)}</option>{/each}
    </select>
  </td>
  <td>
    <input type="number" min="0" placeholder={t('relay.none')} value={u.rate_kbps || ''} onchange={setRate} />
  </td>
  <td class="actions">
    <button onclick={openLimits}>{t('uplink.limitButton')}</button>
    <button class="danger" onclick={() => (asking = true)}>{t('common.delete')}</button>
    {#if editing}
      <ConfirmDialog title={t('uplink.limitsTitle', { name: u.name })} yes={t('common.save')} no={t('common.cancel')} onyes={saveLimits} onno={() => (editing = null)}>
        <div class="form">
          <label>{t('uplink.cap')} <input type="number" min="0" placeholder={t('relay.none')} bind:value={editing.cap} /> MB</label>
          <label>
            {t('uplink.period')}
            <select bind:value={editing.period}>
              {#each periods as v}<option value={v}>{t('uplink.period.' + v)}</option>{/each}
            </select>
          </label>
          {#if editing.period === 'month'}
            <label>{t('uplink.resetDay')} <input type="number" min="1" max="28" bind:value={editing.resetDay} /></label>
          {/if}
          <label class="check">
            <input type="checkbox" bind:checked={editing.throttle} />
            {t('uplink.throttle')}
          </label>
          <label class="check" title={t('uplink.ttlHint')}>
            <input type="checkbox" bind:checked={editing.ttl} />
            {t('uplink.ttl')}
          </label>
          <p class="muted small">
            {t('uplink.usedPeriod', { mb: megabytes(u.used_mb) })}
            <button class="link" onclick={resetUsage}>{t('uplink.reset')}</button>
          </p>
        </div>
      </ConfirmDialog>
    {/if}
    {#if asking}
      <ConfirmDialog title={t('uplink.forgetAsk', { name: u.name })} yes={t('common.deleteYes')} no={t('common.no')} danger onyes={forget}
        onno={() => (asking = false)}>
        <p>{t('uplink.forgetBody')}</p>
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
