<script>
  import { api, errorText } from '../lib/api.js'
  import { mbps } from '../lib/fmt.js'
  import ConfirmDialog from './ConfirmDialog.svelte'

  let { view, notify } = $props()

  const modes = [
    ['max_throughput', 'Maksymalna przepustowość', 'Wszystkie telefony razem — najszybsze pobieranie i wysyłanie.'],
    ['min_latency', 'Minimalne opóźnienie', 'Gry i rozmowy: małe pakiety idą najszybszym telefonem i w dwóch kopiach.'],
  ]
  const redundancies = [
    ['off', 'Wył.', 'Każdy pakiet jedną drogą. Najmniej danych z telefonów.'],
    ['auto', 'Auto', 'Gdy łącze gubi pakiety lub się waha, rozmowy i gry (UDP) idą w dwóch kopiach.'],
    ['on', 'Wł.', 'Każdy pakiet dwiema drogami: odporne na straty, ale zużywa 2× więcej danych z telefonów.'],
  ]

  const secs = 10
  const maxMB = 20
  let target = $state('')
  let asking = $state(false)
  let running = $state(false)
  let result = $state(null)

  let connected = $derived(view.relays.filter((r) => r.connected))
  const atLeastHint = 'Co najmniej tyle: łącze przyjęło wszystko, co test wysłał'
  const rate = (p) => (p.at_least ? '≥ ' : '') + p.mbps.toFixed(1)
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
      notify('Test łączy zakończony')
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
  <h2>Tryb łączenia</h2>
  <div class="grid">
    <div>
      <div class="label">Tryb</div>
      <div class="seg">
        {#each modes as [v, label, hint]}
          <button
            class:active={view.bond_mode === v}
            title={hint}
            onclick={() => setBond(v, '', `Tryb: ${label.toLowerCase()}`)}>{label}</button>
        {/each}
      </div>
      <p class="muted small">{modes.find(([v]) => v === view.bond_mode)?.[2] ?? ''}</p>
    </div>
    <div>
      <div class="label">
        Redundancja
        {#if view.redundant}<span class="chip warn">dubluje teraz</span>{/if}
      </div>
      <div class="seg">
        {#each redundancies as [v, label, hint]}
          <button
            class:active={view.redundancy === v}
            title={hint}
            onclick={() => setBond('', v, `Redundancja: ${label.toLowerCase()}`)}>{label}</button>
        {/each}
      </div>
      <p class="muted small">{redundancies.find(([v]) => v === view.redundancy)?.[2] ?? ''}</p>
    </div>
    <div>
      <div class="label">
        Przyspieszanie TCP
        {#if pepOn && view.pep?.available}<span class="chip">{view.pep.streams} połączeń</span>{/if}
        {#if pepOn && view.pep && !view.pep.available && view.tunnel_up}<span class="chip warn">serwer nie obsługuje</span>{/if}
      </div>
      <div class="seg">
        <button class:active={!pepOn} onclick={() => setPEP(false, false, 'Przyspieszanie TCP wyłączone')}>Wył.</button>
        <button class:active={pepOn} onclick={() => setPEP(true, false, 'Przyspieszanie TCP włączone')}>Wł.</button>
      </div>
      <p class="muted small">
        Hub i serwer kończą połączenia TCP za klienta, a przez telefony idą tylko dane. Pomaga na łączach ze stratami
        i różnym opóźnieniem. Działające połączenia zostają w dotychczasowym trybie.
      </p>
      <div class="test">
        <input
          type="text"
          placeholder="wyjątki: 22, 5000-5100, 10.0.0.0/8"
          title="Porty, zakresy portów i sieci IPv4, które omijają przyspieszanie"
          bind:value={pepExclude}
          disabled={!pepLoaded} />
        <button disabled={!pepLoaded} onclick={() => setPEP(pepOn, true, 'Zapisano wyjątki')}>Zapisz</button>
      </div>
    </div>
    <div>
      <div class="label">Test łączy</div>
      <div class="test">
        <select bind:value={target} disabled={running}>
          <option value="">wszystkie telefony naraz</option>
          {#each connected as r (r.id)}<option value={r.name}>{r.name}</option>{/each}
        </select>
        <button class="primary" disabled={running || view.testing || connected.length === 0} onclick={() => (asking = true)}>
          {running ? 'Testowanie…' : 'Testuj łącza'}
        </button>
      </div>
      <p class="muted small">Mierzy surową przepustowość każdego telefonu ({secs} s pobierania + {secs} s wysyłania).</p>
    </div>
  </div>

  {#if result}
    <table>
      <thead>
        <tr>
          <th>Telefon</th>
          <th class="num">↓ Mb/s</th>
          <th class="num">↑ Mb/s</th>
          <th class="num">RTT</th>
          <th class="num">Straty</th>
        </tr>
      </thead>
      <tbody>
        {#each rows as r (r.name)}
          <tr>
            <td>{r.name || '—'}</td>
            <td class="num down" title={r.down?.at_least ? atLeastHint : ''}>{r.down ? rate(r.down) : '—'}</td>
            <td class="num up" title={r.up?.at_least ? atLeastHint : ''}>{r.up ? rate(r.up) : '—'}</td>
            <td class="num">{r.up?.rtt_ms ?? r.down?.rtt_ms ?? '—'} ms</td>
            <td class="num">{(((r.up?.loss ?? r.down?.loss) || 0) * 100).toFixed(1)}%</td>
          </tr>
        {/each}
        {#if !result.relay}
          <tr class="sum">
            <td>Razem</td>
            <td class="num down">{anyAtLeast('down') ? '≥ ' : ''}{mbps(result.down_mbps)}</td>
            <td class="num up">{anyAtLeast('up') ? '≥ ' : ''}{mbps(result.up_mbps)}</td>
            <td></td>
            <td></td>
          </tr>
        {/if}
      </tbody>
    </table>
    {#if result.paths?.some((p) => p.at_least)}
      <p class="muted small">≥ — łącze przyjęło wszystko, co test wysłał, także w powtórce z większym limitem
        danych, więc jest co najmniej tak szybkie.</p>
    {/if}
  {/if}
</div>

{#if asking}
  <ConfirmDialog title="Testować łącza?" yes="Testuj" onyes={runTest} onno={() => (asking = false)}>
    Test zużyje zwykle do <b>{costMB} MB</b> danych komórkowych
    {target ? `telefonu ${target}` : `z ${connected.length} telefonów (po ${maxMB * 2} MB z każdego)`}.
    Łącze szybsze, niż zmierzy pierwszy pomiar, jest mierzone jeszcze raz z większym limitem — wtedy
    najwyżej <b>{costMaxMB} MB</b> łącznie.
    To dane znajomych — upewnij się, że się zgadzają. W czasie testu połączenie przez ten telefon zwolni.
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
