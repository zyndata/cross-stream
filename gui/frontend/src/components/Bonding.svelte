<script>
  import { api, errorText } from '../lib/api.js'
  import { mbps } from '../lib/fmt.js'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import { t, num } from '../i18n/i18n.svelte.js'

  let { view, notify } = $props()

  // [value, dictionary key] — label at key, hint at key + 'Hint'.
  const modes = [
    ['max_throughput', 'bond.mode.max'],
    ['min_latency', 'bond.mode.min'],
  ]
  const redundancies = [
    ['off', 'bond.red.off'],
    ['auto', 'bond.red.auto'],
    ['on', 'bond.red.on'],
  ]
  const hintOf = (list, v) => {
    const k = list.find(([x]) => x === v)?.[1]
    return k ? t(k + 'Hint') : ''
  }

  const secs = 10
  const maxMB = 20
  let target = $state('')
  let asking = $state(false)
  let running = $state(false)
  let result = $state(null)

  let connected = $derived(view.relays.filter((r) => r.connected))
  const rate = (p) => (p.at_least ? '≥ ' : '') + num(p.mbps, 1)
  const anyAtLeast = (dir) => result?.paths?.some((p) => p.dir === dir && p.at_least)
  const retryFactor = 3 // hub.RetryFactor: a path faster than the first round is measured again
  let costMB = $derived((target ? 1 : connected.length) * maxMB * 2)
  let costMaxMB = $derived(costMB * (1 + retryFactor))

  async function setBond(mode, redundancy, what) {
    try {
      await api.setBond(mode, redundancy)
      notify(what)
    } catch (e) {
      notify(errorText(e), true)
    }
  }

  // PEP (split TCP): the exclusions are loaded once and edited as text.
  let pepExclude = $state('')
  let pepLoaded = $state(false)
  let pepOn = $derived(view.pep?.on ?? false)

  async function loadPEP() {
    try {
      const s = await api.pepSettings()
      pepExclude = (s.pep_exclude ?? []).join(', ')
      pepLoaded = true
    } catch (e) {
      notify(errorText(e), true)
    }
  }
  $effect(() => {
    if (!pepLoaded) loadPEP()
  })

  function excludeList() {
    return pepExclude
      .split(/[,\s]+/)
      .map((s) => s.trim())
      .filter((s) => s !== '')
  }

  async function setPEP(on, withList, what) {
    try {
      await api.setPEP(on, withList ? excludeList() : null)
      notify(what)
    } catch (e) {
      notify(errorText(e), true)
    }
  }

  async function runTest() {
    asking = false
    running = true
    result = null
    try {
      result = await api.speedTest({ relay: target, dir: 'both', secs, max_mb: maxMB })
      notify(t('bond.test.done'))
    } catch (e) {
      notify(errorText(e), true)
    }
    running = false
  }

  function row(name) {
    const down = result.paths.find((p) => p.relay === name && p.dir === 'down')
    const up = result.paths.find((p) => p.relay === name && p.dir === 'up')
    return { name, down, up }
  }
  let rows = $derived(result ? [...new Set(result.paths.map((p) => p.relay))].map(row) : [])
</script>

<div class="card bonding">
  <h2>{t('bond.title')}</h2>
  <div class="grid">
    <div>
      <div class="label">{t('bond.mode')}</div>
      <div class="seg">
        {#each modes as [v, key]}
          <button
            class:active={view.bond_mode === v}
            title={t(key + 'Hint')}
            onclick={() => setBond(v, '', t('bond.modeToast', { mode: t(key).toLowerCase() }))}>{t(key)}</button>
        {/each}
      </div>
      <p class="muted small">{hintOf(modes, view.bond_mode)}</p>
    </div>
    <div>
      <div class="label">
        {t('bond.redundancy')}
        {#if view.redundant}<span class="chip warn">{t('bond.redNow')}</span>{/if}
      </div>
      <div class="seg">
        {#each redundancies as [v, key]}
          <button
            class:active={view.redundancy === v}
            title={t(key + 'Hint')}
            onclick={() => setBond('', v, t('bond.redToast', { mode: t(key).toLowerCase() }))}>{t(key)}</button>
        {/each}
      </div>
      <p class="muted small">{hintOf(redundancies, view.redundancy)}</p>
    </div>
    <div>
      <div class="label">
        {t('bond.pep')}
        {#if pepOn && view.pep?.available}<span class="chip">{t('bond.pepStreams', { n: view.pep.streams })}</span>{/if}
        {#if pepOn && view.pep && !view.pep.available && view.tunnel_up}<span class="chip warn">{t('bond.pepNoServer')}</span>{/if}
      </div>
      <div class="seg">
        <button class:active={!pepOn} onclick={() => setPEP(false, false, t('bond.pepOffToast'))}>{t('common.off')}</button>
        <button class:active={pepOn} onclick={() => setPEP(true, false, t('bond.pepOnToast'))}>{t('common.on')}</button>
      </div>
      <p class="muted small">
        {t('bond.pepHint')}
      </p>
      <div class="test">
        <input
          type="text"
          placeholder={t('bond.pepExcludePh')}
          title={t('bond.pepExcludeHint')}
          bind:value={pepExclude}
          disabled={!pepLoaded} />
        <button disabled={!pepLoaded} onclick={() => setPEP(pepOn, true, t('bond.pepExcludeSaved'))}>{t('common.save')}</button>
      </div>
    </div>
    <div>
      <div class="label">{t('bond.test')}</div>
      <div class="test">
        <select bind:value={target} disabled={running}>
          <option value="">{t('bond.test.all')}</option>
          {#each connected as r (r.id)}<option value={r.name}>{r.name}</option>{/each}
        </select>
        <button class="primary" disabled={running || view.testing || connected.length === 0} onclick={() => (asking = true)}>
          {running ? t('bond.test.running') : t('bond.test.run')}
        </button>
      </div>
      <p class="muted small">{t('bond.test.hint', { secs })}</p>
    </div>
  </div>

  {#if result}
    <table>
      <thead>
        <tr>
          <th>{t('common.phone')}</th>
          <th class="num">↓ Mb/s</th>
          <th class="num">↑ Mb/s</th>
          <th class="num">RTT</th>
          <th class="num">{t('bond.test.loss')}</th>
        </tr>
      </thead>
      <tbody>
        {#each rows as r (r.name)}
          <tr>
            <td>{r.name || '—'}</td>
            <td class="num down" title={r.down?.at_least ? t('bond.test.atLeast') : ''}>{r.down ? rate(r.down) : '—'}</td>
            <td class="num up" title={r.up?.at_least ? t('bond.test.atLeast') : ''}>{r.up ? rate(r.up) : '—'}</td>
            <td class="num">{r.up?.rtt_ms ?? r.down?.rtt_ms ?? '—'} ms</td>
            <td class="num">{num(((r.up?.loss ?? r.down?.loss) || 0) * 100, 1)}%</td>
          </tr>
        {/each}
        {#if !result.relay}
          <tr class="sum">
            <td>{t('bond.test.total')}</td>
            <td class="num down">{anyAtLeast('down') ? '≥ ' : ''}{mbps(result.down_mbps)}</td>
            <td class="num up">{anyAtLeast('up') ? '≥ ' : ''}{mbps(result.up_mbps)}</td>
            <td></td>
            <td></td>
          </tr>
        {/if}
      </tbody>
    </table>
    {#if result.paths?.some((p) => p.at_least)}
      <p class="muted small">{t('bond.test.atLeastNote')}</p>
    {/if}
  {/if}
</div>

{#if asking}
  <ConfirmDialog title={t('bond.test.ask')} yes={t('bond.test.yes')} onyes={runTest} onno={() => (asking = false)}>
    {t('bond.test.cost1')} <b>{costMB} MB</b> {target ? t('bond.test.costOne', { name: target }) : t('bond.test.costAll', { n: connected.length, each: maxMB * 2 })}.
    {t('bond.test.cost2')} <b>{costMaxMB} MB</b> {t('bond.test.cost3')}
  </ConfirmDialog>
{/if}

<style>
  .bonding {
    margin-top: 14px;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
    gap: 16px;
  }
  .label {
    font-weight: 600;
    margin-bottom: 6px;
    display: flex;
    gap: 8px;
    align-items: center;
  }
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
  .test {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    margin-top: 14px;
  }
  th,
  td {
    text-align: left;
    padding: 5px;
    border-bottom: 1px solid var(--line);
  }
  th {
    font-size: 12px;
    color: var(--muted);
    font-weight: 600;
  }
  .num {
    text-align: right;
  }
  .up {
    color: var(--up);
  }
  .down {
    color: var(--down);
  }
  .sum td {
    font-weight: 600;
  }
</style>
