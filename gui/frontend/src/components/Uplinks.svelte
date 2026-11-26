<script>
  import UplinkRow from './UplinkRow.svelte'
  import { t } from '../i18n/i18n.svelte.js'

  let { view, notify } = $props()
  const links = $derived(view.uplinks ?? [])
</script>

<div class="card">
  <h2>{t('uplinks.title', { n: links.filter((u) => u.state === 'up').length, total: links.length })}</h2>
  {#if links.length === 0}
    <p class="muted">
      {t('uplinks.none')}
    </p>
  {:else}
    <p class="muted small">
      {t('uplinks.newOff')}
    </p>
    <div class="scroll">
      <table>
        <thead>
          <tr>
            <th>{t('uplinks.col.use')}</th>
            <th>{t('relays.col.link')}</th>
            <th>{t('relays.col.state')}</th>
            <th class="num">↑ / ↓</th>
            <th class="num">{t('uplinks.col.rtt')}</th>
            <th class="num">{t('relays.col.used')}</th>
            <th>{t('relays.col.prio')}</th>
            <th>{t('relays.col.up')}</th>
            <th>{t('relays.col.down')}</th>
            <th title={t('relays.col.rateHint')}>{t('relays.col.rate')}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each links as u (u.id)}
            <UplinkRow {u} {notify} />
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<style>
  .scroll {
    overflow-x: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  th {
    text-align: left;
    font-weight: 600;
    font-size: 12px;
    color: var(--muted);
    border-bottom: 1px solid var(--line);
    padding: 6px 5px;
    white-space: nowrap;
  }
  th.num {
    text-align: right;
  }
</style>
