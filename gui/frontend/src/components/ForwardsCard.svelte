<script>
  import { onMount } from 'svelte'
  import { api, errorText } from '../lib/api.js'
  import { t } from '../i18n/i18n.svelte.js'

  let { notify } = $props()
  let info = $state(null)
  let rows = $state([])
  let busy = $state(false)

  const states = ['active', 'pending', 'refused', 'no_client', 'not_online']
  const reasons = ['not_allowed', 'restricted', 'out_of_range', 'in_use']
  const stateLabel = (s) => (states.includes(s) ? t('fwd.state.' + s) : s)
  const reasonLabel = (r) => (reasons.includes(r) ? t('fwd.reason.' + r) : r)

  function fill(i) {
    info = i
    rows = (i.forwards ?? []).map((f) => ({ proto: f.proto, port: f.port, target: f.target, name: f.name ?? '' }))
  }
  onMount(() => api.forwards().then(fill).catch((e) => notify(errorText(e), true)))

  const add = () => (rows = [...rows, { proto: 'tcp', port: 8080, target: '', name: '' }])
  const remove = (i) => (rows = rows.filter((_, j) => j !== i))

  async function save() {
    busy = true
    try {
      fill(await api.setForwards(rows.map((r) => ({ ...r, port: Number(r.port) || 0 }))))
      notify(t('fwd.saved'))
    } catch (e) {
      notify(errorText(e), true)
    } finally {
      busy = false
    }
  }
  const status = (i) => info?.forwards?.[i]
</script>

<div class="card">
  <h2>{t('fwd.title')}</h2>
  <p class="small">
    {t('fwd.hint1')}{#if info?.server} <code>{info.server}</code>{/if} {t('fwd.hint2')}
  </p>
  {#if rows.length}
    <table>
      <thead><tr><th>Proto</th><th>Port</th><th>{t('fwd.col.device')}</th><th>{t('fwd.col.name')}</th><th>{t('relays.col.state')}</th><th></th></tr></thead>
      <tbody>
        {#each rows as r, i}
          <tr>
            <td>
              <select bind:value={r.proto}><option value="tcp">TCP</option><option value="udp">UDP</option></select>
            </td>
            <td><input class="port" type="number" min="1024" max="49151" bind:value={r.port} /></td>
            <td><input bind:value={r.target} placeholder="aa:bb:cc:dd:ee:ff" spellcheck="false" /></td>
            <td><input bind:value={r.name} placeholder={t('fwd.namePh')} /></td>
            <td class="small">
              {#if status(i) && status(i).port === Number(r.port) && status(i).proto === r.proto}
                <span class:ok={status(i).state === 'active'} class:bad={status(i).state === 'refused'}>
                  {stateLabel(status(i).state)}
                </span>
                {#if status(i).reason}<div class="muted">{reasonLabel(status(i).reason)}</div>{/if}
                {#if status(i).client}<div class="muted">{status(i).client}</div>{/if}
              {:else}<span class="muted">{t('fwd.unsaved')}</span>{/if}
            </td>
            <td><button onclick={() => remove(i)} title={t('common.delete')}>✕</button></td>
          </tr>
        {/each}
      </tbody>
    </table>
  {:else}
    <p class="muted small">{t('fwd.none')}</p>
  {/if}
  <div class="row">
    <button onclick={add} disabled={rows.length >= 20}>{t('common.add')}</button>
    <button class="primary" onclick={save} disabled={busy}>{busy ? t('diag.saving') : t('common.save')}</button>
  </div>
</div>

<style>
  table {
    width: 100%;
    border-collapse: collapse;
    margin: 8px 0;
  }
  th {
    text-align: left;
    font-size: 12px;
    color: var(--muted);
    font-weight: normal;
  }
  td {
    padding: 3px 4px 3px 0;
    vertical-align: top;
  }
  input {
    width: 100%;
    color: var(--text);
  }
  .port {
    width: 80px;
  }
  .row {
    display: flex;
    gap: 8px;
  }
  .ok {
    color: var(--ok);
  }
  .bad {
    color: var(--bad);
  }
</style>
