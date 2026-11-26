<script>
  import Chart from './Chart.svelte'
  import { mbps } from '../lib/format.js'
  import { t } from '../i18n/i18n.svelte.js'

  let { history = [], sample = null } = $props()
  let perHub = $derived(
    Object.entries(sample?.hubs || {})
      .map(([name, [rx, tx]]) => ({ name, rx, tx }))
      .sort((a, b) => b.rx + b.tx - (a.rx + a.tx)),
  )
</script>

<div class="tiles">
  <div class="card tile">
    <div class="muted small">{t('dash.online')}</div>
    <div class="big">{sample ? sample.online : '—'} <span class="muted">/ {sample ? sample.total : '—'}</span></div>
  </div>
  <div class="card tile">
    <div class="muted small">{t('dash.fromHubs')}</div>
    <div class="big up">{mbps(sample?.rx)}</div>
  </div>
  <div class="card tile">
    <div class="muted small">{t('dash.toHubs')}</div>
    <div class="big down">{mbps(sample?.tx)}</div>
  </div>
</div>

<Chart {history} title={t('chart.title')} />

<div class="card">
  <h2>{t('dash.perHub')}</h2>
  {#if perHub.length === 0}
    <p class="muted">{t('dash.none')}</p>
  {:else}
    <table>
      <thead><tr><th>Hub</th><th>{t('hub.fromHub')}</th><th>{t('hub.toHub')}</th></tr></thead>
      <tbody>
        {#each perHub as h (h.name)}
          <tr><td><a href="#/hubs/{encodeURIComponent(h.name)}">{h.name}</a></td><td>{mbps(h.rx)}</td><td>{mbps(h.tx)}</td></tr>
        {/each}
      </tbody>
    </table>
  {/if}
</div>

<style>
  .tiles {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 12px;
  }
  .big {
    font-size: 24px;
    font-weight: 600;
    margin-top: 4px;
  }
  .big.up {
    color: var(--up);
  }
  .big.down {
    color: var(--down);
  }
</style>
