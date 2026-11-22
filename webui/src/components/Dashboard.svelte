<script>
  import Chart from './Chart.svelte'
  import { mbps } from '../lib/format.js'

  let { history = [], sample = null } = $props()
  let perHub = $derived(
    Object.entries(sample?.hubs || {})
      .map(([name, [rx, tx]]) => ({ name, rx, tx }))
      .sort((a, b) => b.rx + b.tx - (a.rx + a.tx)),
  )
</script>

<div class="tiles">
  <div class="card tile">
    <div class="muted small">Huby online</div>
    <div class="big">{sample ? sample.online : '—'} <span class="muted">/ {sample ? sample.total : '—'}</span></div>
  </div>
  <div class="card tile">
    <div class="muted small">Od hubów (upload)</div>
    <div class="big up">{mbps(sample?.rx)}</div>
  </div>
  <div class="card tile">
    <div class="muted small">Do hubów (download)</div>
    <div class="big down">{mbps(sample?.tx)}</div>
  </div>
</div>

<Chart {history} />

<div class="card">
  <h2>Przepustowość hubów (teraz)</h2>
  {#if perHub.length === 0}
    <p class="muted">Żaden hub nie jest połączony.</p>
  {:else}
    <table>
      <thead><tr><th>Hub</th><th>Od huba</th><th>Do huba</th></tr></thead>
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
