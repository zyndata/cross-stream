<script>
  import { api } from '../lib/api.js'
  import { t } from '../i18n/i18n.svelte.js'

  let { hubs = [], onchange, toast } = $props()
  let edits = $state({})

  async function save(name) {
    const n = Number(edits[name])
    try {
      await api.forward(name, n)
      toast(t('fwd.saved', { name, n }))
      delete edits[name]
    } catch (err) {
      toast(err.message, true)
    }
    onchange()
  }
</script>

<div class="card">
  <h2>{t('fwd.title')}</h2>
  <p class="muted small">
    {t('fwd.hint')}
  </p>
  {#if hubs.length === 0}
    <p class="muted">{t('fwd.noHubs')}</p>
  {:else}
    <div class="scroll">
      <table>
        <thead><tr><th>Hub</th><th>{t('fwd.col.limit')}</th><th>{t('fwd.col.active')}</th></tr></thead>
        <tbody>
          {#each hubs as h (h.name)}
            <tr>
              <td>{h.name}</td>
              <td class="allow">
                <input type="number" min="0" max="20" value={edits[h.name] ?? h.fwd_allow}
                  oninput={(e) => (edits[h.name] = e.currentTarget.value)} />
                {#if edits[h.name] !== undefined && edits[h.name] !== '' && Number.isInteger(Number(edits[h.name])) && Number(edits[h.name]) !== h.fwd_allow}
                  <button class="primary" onclick={() => save(h.name)}>{t('common.save')}</button>
                {/if}
              </td>
              <td>
                {#each h.forwards || [] as f}
                  <span class="chip">{f.proto}/{f.port}</span>
                {:else}
                  <span class="muted">—</span>
                {/each}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<style>
  .allow {
    white-space: nowrap;
  }
  .allow input {
    width: 70px;
  }
</style>
