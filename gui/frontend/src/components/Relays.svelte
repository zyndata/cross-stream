<script>
  import { api, errorText } from '../lib/api.js'
  import RelayRow from './RelayRow.svelte'
  import { t } from '../i18n/i18n.svelte.js'

  let { view, notify } = $props()
  let overflow = $state('')
  let editing = $state(false)

  $effect(() => {
    if (!editing) overflow = String(view.overflow_mbps ?? 0)
  })

  async function saveOverflow() {
    const v = Number(overflow)
    if (!Number.isInteger(v) || v < 0 || v > 65535) {
      notify(t('relays.overflowError'), true)
      return
    }
    try {
      await api.setOverflow(v)
      notify(v ? t('relays.overflowSet', { n: v }) : t('relays.overflowOff'))
    } catch (e) {
      notify(errorText(e), true)
    }
    editing = false
  }
</script>

<div class="card">
  <h2>{t('relays.title', { n: view.relays.filter((r) => r.connected).length, total: view.relays.length })}</h2>
  {#if view.relays.length === 0}
    <p class="muted">{t('relays.none')}</p>
  {:else}
    <div class="scroll">
      <table>
        <thead>
          <tr>
            <th>{t('common.phone')}</th>
            <th>{t('relays.col.state')}</th>
            <th title={t('relays.col.linkHint')}>{t('relays.col.link')}</th>
            <th class="num">↑ / ↓</th>
            <th class="num">{t('relays.col.rtt')}</th>
            <th class="num">{t('relays.col.used')}</th>
            <th>{t('relays.col.prio')}</th>
            <th>{t('relays.col.up')}</th>
            <th>{t('relays.col.down')}</th>
            <th title={t('relays.col.rateHint')}>{t('relays.col.rate')}</th>
            <th title={t('relays.col.capHint')}>{t('relays.col.cap')}</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each view.relays as rv (rv.id)}
            <RelayRow {rv} {notify} />
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
  <div class="overflow">
    <label for="overflow">{t('relays.overflow')}</label>
    <input
      id="overflow"
      type="number"
      min="0"
      max="65535"
      bind:value={overflow}
      onfocus={() => (editing = true)}
      onkeydown={(e) => e.key === 'Enter' && saveOverflow()}
    />
    <span>Mb/s</span>
    <button onclick={saveOverflow}>{t('common.save')}</button>
    <span class="muted small">{t('relays.overflowZero')}</span>
  </div>
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
  .overflow {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-top: 14px;
    flex-wrap: wrap;
  }
  .overflow input {
    width: 80px;
  }
</style>
