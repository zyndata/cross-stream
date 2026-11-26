<script>
  import { api } from '../lib/api.js'
  import { when } from '../lib/format.js'
  import { t } from '../i18n/i18n.svelte.js'

  let entries = $state([])
  let error = $state('')
  function load() {
    api.audit(200).then((a) => (entries = a || []), (e) => (error = e.message))
  }
  load()

  const actions = ['enroll', 'revoke', 'rename', 'forward_allow', 'login', 'logout', 'login_lockout', 'admin_add',
    'admin_passwd', 'admin_del', 'admin_totp', 'tunnel_port', 'restart']
  const action = (a) => (actions.includes(a) ? t('audit.action.' + a) : a)
</script>

<div class="card">
  <h2>{t('audit.title')} <button class="refresh" onclick={load}>{t('common.refresh')}</button></h2>
  {#if error}<div class="banner bad">{error}</div>{/if}
  <div class="scroll">
    <table>
      <thead><tr><th>{t('audit.col.time')}</th><th>{t('audit.col.who')}</th><th>{t('audit.col.action')}</th><th>{t('audit.col.target')}</th><th>{t('audit.col.result')}</th></tr></thead>
      <tbody>
        {#each entries as e}
          <tr>
            <td class="small nowrap">{when(e.time)}</td>
            <td class="small">{e.actor}</td>
            <td>{action(e.action)}</td>
            <td>{e.target || ''} <span class="muted small">{e.detail || ''}</span></td>
            <td>{#if e.ok}<span class="chip">ok</span>{:else}<span class="chip bad">{e.error || t('audit.error')}</span>{/if}</td>
          </tr>
        {:else}
          <tr><td colspan="5" class="muted">{t('audit.none')}</td></tr>
        {/each}
      </tbody>
    </table>
  </div>
</div>

<style>
  h2 {
    display: flex;
    align-items: center;
  }
  .refresh {
    margin-left: auto;
    font-size: 13px;
  }
  .nowrap {
    white-space: nowrap;
  }
</style>
