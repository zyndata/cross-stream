<script>
  import { onMount } from 'svelte'
  import { api, errorText } from '../lib/api.js'
  import { megabytes } from '../lib/fmt.js'

  let { notify } = $props()
  let rep = $state(null)
  const DAYS = 30

  async function load() {
    try {
      rep = await api.usage('')
    } catch (e) {
      notify(errorText(e), true)
    }
  }
  onMount(() => {
    load()
    const t = setInterval(load, 60_000)
    return () => clearInterval(t)
  })

  const pad = (n) => String(n).padStart(2, '0')
  const dayKey = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
  const monthStart = $derived(dayKey(new Date()).slice(0, 8) + '01')
  const mb = (b) => b / 1e6

  // Per friend: this month and all time (bytes up + down), top sharers first.
  const friends = $derived(
    (rep?.relays ?? []).map((r) => ({
      name: r.name || r.id,
      all: r.up + r.down,
      month: r.days.filter((d) => d.date >= monthStart).reduce((s, d) => s + d.up + d.down, 0),
    })),
  )
  const monthTotal = $derived(friends.reduce((s, f) => s + f.month, 0))

  // Last DAYS days, total of all friends per day, with the per-friend split for the tooltip.
  const days = $derived.by(() => {
    const out = []
    const now = new Date()
    for (let i = DAYS - 1; i >= 0; i--) {
      const d = new Date(now.getFullYear(), now.getMonth(), now.getDate() - i)
      out.push({ date: dayKey(d), total: 0, parts: [] })
    }
    const idx = new Map(out.map((d, i) => [d.date, i]))
    for (const r of rep?.relays ?? []) {
      for (const d of r.days) {
        const i = idx.get(d.date)
        if (i === undefined) continue
        out[i].total += d.up + d.down
        out[i].parts.push(`${r.name || r.id}: ${megabytes(mb(d.up + d.down))}`)
      }
    }
    return out
  })
  const peak = $derived(Math.max(1, ...days.map((d) => d.total)))
  const W = 600
  const H = 120
</script>

<div class="card">
  <h2>Statystyki</h2>
  {#if !rep}
    <p class="muted">Ładuję…</p>
  {:else if !friends.length}
    <p class="muted">Jeszcze brak historii — pojawi się, gdy telefony zaczną udostępniać dane.</p>
  {:else}
    <p>
      W tym miesiącu telefony udostępniły <b>{megabytes(mb(monthTotal))}</b>.
      Bezprzerwowe przełączenia przy odejściu telefonu: <b>{rep.failovers}</b>.
    </p>
    <table>
      <thead><tr><th>#</th><th>Telefon</th><th class="num">Ten miesiąc</th><th class="num">Od początku</th></tr></thead>
      <tbody>
        {#each friends as f, i}
          <tr>
            <td>{i + 1}</td>
            <td>{f.name}</td>
            <td class="num">{megabytes(mb(f.month))}</td>
            <td class="num">{megabytes(mb(f.all))}</td>
          </tr>
        {/each}
      </tbody>
    </table>
    <h3>Ostatnie {DAYS} dni</h3>
    <svg viewBox="0 0 {W} {H + 16}" role="img" aria-label="Dane udostępnione na dzień">
      {#each days as d, i}
        {@const h = (d.total / peak) * H}
        <rect x={(i * W) / DAYS + 1} y={H - h} width={W / DAYS - 2} height={Math.max(h, d.total ? 1 : 0)} rx="2">
          <title>{d.date}: {megabytes(mb(d.total))}{d.parts.length ? '\n' + d.parts.join('\n') : ''}</title>
        </rect>
      {/each}
      <line x1="0" x2={W} y1={H} y2={H} />
      <text x="0" y={H + 13}>{days[0].date}</text>
      <text x={W} y={H + 13} text-anchor="end">dziś</text>
    </svg>
    {#if rep.speed_tests?.length}
      <p class="muted small">Ostatni test łączy: {rep.speed_tests[0].down_mbps} Mb/s pobierania, {rep.speed_tests[0].up_mbps} Mb/s wysyłania.</p>
    {/if}
  {/if}
</div>

<style>
  table {
    width: 100%;
    border-collapse: collapse;
    margin: 8px 0 14px;
  }
  th,
  td {
    text-align: left;
    padding: 5px 6px;
    border-bottom: 1px solid var(--line);
  }
  th {
    font-weight: normal;
    color: var(--muted);
    font-size: 12px;
  }
  .num {
    text-align: right;
  }
  h3 {
    font-size: 14px;
    margin: 6px 0;
  }
  svg {
    width: 100%;
    height: auto;
  }
  rect {
    fill: var(--accent);
  }
  line {
    stroke: var(--line);
  }
  text {
    fill: var(--muted);
    font-size: 10px;
  }
</style>
