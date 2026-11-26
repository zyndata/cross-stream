<script>
  // Total server throughput, last 5 minutes (one sample per second, bytes/s).
  // rx = hubs → server (upload), tx = server → hubs (download).
  import { t, num } from '../i18n/i18n.svelte.js'

  let { history = [], title = '' } = $props()

  const H = 230
  const L = 64
  const R = 30
  const B = 20
  let width = $state(640)
  let W = $derived(Math.max(300, width))
  const SPAN = 300_000

  function niceMax(v) {
    if (v <= 1) return 1
    const p = 10 ** Math.floor(Math.log10(v))
    for (const m of [1, 2, 5, 10]) if (v <= m * p) return m * p
    return 10 * p
  }

  let pts = $derived(history.map((s) => ({ t: s.t, up: (s.rx * 8) / 1e6, down: (s.tx * 8) / 1e6 })))
  let end = $derived(pts.length ? pts[pts.length - 1].t : Date.now())
  let max = $derived(niceMax(Math.max(0, ...pts.map((s) => Math.max(s.up, s.down)))))

  function x(t) {
    return L + ((t - (end - SPAN)) / SPAN) * (W - L - R)
  }
  function y(v) {
    return H - B - (v / max) * (H - B - 8)
  }
  function line(key) {
    return pts.map((s) => `${x(s.t).toFixed(1)},${y(s[key]).toFixed(1)}`).join(' ')
  }
  let ticks = $derived([0, max / 2, max])
</script>

<div class="card">
  <h2>
    {title || t('chart.title')}
    <span class="legend"><i class="up"></i>{t('chart.fromHubs')} <i class="down"></i>{t('chart.toHubs')}</span>
  </h2>
  <div bind:clientWidth={width}>
    <svg width={W} height={H} viewBox="0 0 {W} {H}" role="img" aria-label={t('chart.label')}>
      {#each ticks as v}
        <line x1={L} x2={W - R} y1={y(v)} y2={y(v)} class="grid" />
        <text x={L - 6} y={y(v) + 4} text-anchor="end">{v === 0 ? '0' : num(v, v < 1 ? 1 : 0)} Mb/s</text>
      {/each}
      {#each [0, 1, 2, 3, 4, 5] as m}
        <text x={x(end - SPAN + m * 60_000)} y={H - 3} text-anchor="middle">{m === 5 ? t('chart.now') : `-${5 - m} min`}</text>
      {/each}
      {#if pts.length > 1}
        <polyline points={line('down')} class="down" />
        <polyline points={line('up')} class="up" />
      {/if}
    </svg>
  </div>
</div>

<style>
  svg {
    display: block;
  }
  .grid {
    stroke: var(--line);
    stroke-width: 1;
  }
  text {
    fill: var(--muted);
    font-size: 11px;
  }
  polyline {
    fill: none;
    stroke-width: 2;
    stroke-linejoin: round;
  }
  polyline.up {
    stroke: var(--up);
  }
  polyline.down {
    stroke: var(--down);
  }
  .legend {
    font-weight: normal;
    font-size: 12px;
    color: var(--muted);
    margin-left: 12px;
  }
  .legend i {
    display: inline-block;
    width: 14px;
    height: 3px;
    margin: 0 4px 3px 10px;
    vertical-align: middle;
  }
  i.up {
    background: var(--up);
  }
  i.down {
    background: var(--down);
  }
</style>
