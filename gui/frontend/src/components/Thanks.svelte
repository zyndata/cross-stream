<script>
  import { onMount } from 'svelte'
  import { api, errorText } from '../lib/api.js'
  import { megabytes } from '../lib/fmt.js'
  import { t } from '../i18n/i18n.svelte.js'

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
    const timer = setInterval(load, 60_000)
    return () => clearInterval(timer)
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
  <h2>{t('app.tab.stats')}</h2>
  {#if !rep}
    <p class="muted">{t('common.loading')}</p>
  {:else if !friends.length}
    <p class="muted">{t('stats.empty')}</p>
  {:else}
    <p>
      {t('stats.month')} <b>{megabytes(mb(monthTotal))}</b>.
      {t('stats.failovers')} <b>{rep.failovers}</b>.
    </p>
    <table>
      <thead><tr><th>#</th><th>{t('common.phone')}</th><th class="num">{t('stats.thisMonth')}</th><th class="num">{t('stats.allTime')}</th></tr></thead>
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
    <h3>{t('stats.lastDays', { n: DAYS })}</h3>
    <svg viewBox="0 0 {W} {H + 16}" role="img" aria-label={t('stats.chartLabel')}>
      {#each days as d, i}
        {@const h = (d.total / peak) * H}
        <rect x={(i * W) / DAYS + 1} y={H - h} width={W / DAYS - 2} height={Math.max(h, d.total ? 1 : 0)} rx="2">
          <title>{d.date}: {megabytes(mb(d.total))}{d.parts.length ? '\n' + d.parts.join('\n') : ''}</title>
        </rect>
      {/each}
      <line x1="0" x2={W} y1={H} y2={H} />
      <text x="0" y={H + 13}>{days[0].date}</text>
      <text x={W} y={H + 13} text-anchor="end">{t('stats.today')}</text>
    </svg>
    {#if rep.speed_tests?.length}
      <p class="muted small">{t('stats.lastTest', { down: rep.speed_tests[0].down_mbps, up: rep.speed_tests[0].up_mbps })}</p>
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
