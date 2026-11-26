<script>
  import ConfirmDialog from './ConfirmDialog.svelte'
  import EnrollDialog from './EnrollDialog.svelte'
  import RenameDialog from './RenameDialog.svelte'
  import { api } from '../lib/api.js'
  import { mbps, when } from '../lib/format.js'
  import { t } from '../i18n/i18n.svelte.js'

  let { hubs = [], sessions = [], onchange, toast } = $props()
  let enrolling = $state(false)
  let revoking = $state(null)
  let renaming = $state(null)

  let byHub = $derived(Object.fromEntries(sessions.map((s) => [s.hub, s])))
  let stale = $derived(hubs.filter((h) => h.reenroll))

  async function revoke(name) {
    revoking = null
    try {
      await api.revoke(name)
      toast(t('hubs.revoked', { name }))
    } catch (err) {
      toast(err.message, true)
    }
    onchange()
  }

  async function rename(name, newName) {
    renaming = null
    try {
      await api.rename(name, newName)
      toast(t('hubs.renamed', { name, newName }))
    } catch (err) {
      toast(err.message, true)
    }
    onchange()
  }
</script>

<div class="card">
  <h2>
    {t('nav.hubs')}
    <button class="primary add" onclick={() => (enrolling = true)}>{t('hubs.add')}</button>
  </h2>
  {#if stale.length}
    <div class="banner bad">
      {t('hubs.stale', { names: stale.map((h) => h.name).join(', ') })}
    </div>
  {/if}
  {#if hubs.length === 0}
    <p class="muted">{t('hubs.none')}</p>
  {:else}
    <div class="scroll">
      <table>
        <thead>
          <tr><th>{t('hubs.col.name')}</th><th>{t('hubs.col.state')}</th><th>{t('hub.tunnelIp')}</th><th>{t('hubs.col.paths')}</th><th>{t('hub.fromHub')}</th><th>{t('hub.toHub')}</th><th>{t('hubs.col.added')}</th><th></th></tr>
        </thead>
        <tbody>
          {#each hubs as h (h.name)}
            {@const s = byHub[h.name]}
            <tr>
              <td><a href="#/hubs/{encodeURIComponent(h.name)}">{h.name}</a></td>
              <td>
                <span class="chip" class:ok={h.online}>{h.online ? 'online' : 'offline'}</span>
                {#if h.reenroll}<span class="chip bad" title={t('hubs.enrolledPort', { port: h.port })}>{t('hubs.reenroll')}</span>{/if}
              </td>
              <td>{h.tunnel_ip}</td>
              <td>{s ? s.paths?.length || 0 : '—'}</td>
              <td>{s ? mbps(s.rx_rate) : '—'}</td>
              <td>{s ? mbps(s.tx_rate) : '—'}</td>
              <td class="small">{when(h.enrolled)}</td>
              <td class="actions">
                <button onclick={() => (renaming = h.name)}>{t('hubs.rename')}</button>
                <button class="danger" onclick={() => (revoking = h.name)}>{t('common.delete')}</button>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

{#if enrolling}
  <EnrollDialog onclose={() => (enrolling = false)} ondone={onchange} />
{/if}
{#if revoking}
  <ConfirmDialog title={t('hubs.revokeAsk')} yes={t('common.delete')} no={t('common.cancel')} danger onyes={() => revoke(revoking)} onno={() => (revoking = null)}>
    Hub <b>{revoking}</b> {t('hubs.revokeBody')}
  </ConfirmDialog>
{/if}
{#if renaming}
  <RenameDialog name={renaming} onrename={(n) => rename(renaming, n)} onclose={() => (renaming = null)} />
{/if}

<style>
  h2 {
    display: flex;
    align-items: center;
  }
  .add {
    margin-left: auto;
    font-size: 13px;
  }
  .actions {
    white-space: nowrap;
    text-align: right;
  }
  .actions button {
    margin-left: 4px;
  }
  .chip.ok {
    color: var(--ok);
  }
  .chip.bad {
    color: var(--bad);
  }
</style>
