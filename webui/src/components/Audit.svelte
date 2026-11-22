<script>
  import { api } from '../lib/api.js'
  import { when } from '../lib/format.js'

  let entries = $state([])
  let error = $state('')
  function load() {
    api.audit(200).then((a) => (entries = a || []), (e) => (error = e.message))
  }
  load()

  const actions = {
    enroll: 'kod rejestracji', revoke: 'usunięcie huba', rename: 'zmiana nazwy', forward_allow: 'limit przekierowań',
    login: 'logowanie', logout: 'wylogowanie', login_lockout: 'blokada logowania', admin_add: 'nowy administrator',
    admin_passwd: 'zmiana hasła', admin_del: 'usunięcie administratora', admin_totp: '2FA',
  }
</script>

<div class="card">
  <h2>Dziennik zmian <button class="refresh" onclick={load}>Odśwież</button></h2>
  {#if error}<div class="banner bad">{error}</div>{/if}
  <div class="scroll">
    <table>
      <thead><tr><th>Czas</th><th>Kto</th><th>Akcja</th><th>Cel</th><th>Wynik</th></tr></thead>
      <tbody>
        {#each entries as e}
          <tr>
            <td class="small nowrap">{when(e.time)}</td>
            <td class="small">{e.actor}</td>
            <td>{actions[e.action] || e.action}</td>
            <td>{e.target || ''} <span class="muted small">{e.detail || ''}</span></td>
            <td>{#if e.ok}<span class="chip">ok</span>{:else}<span class="chip bad">{e.error || 'błąd'}</span>{/if}</td>
          </tr>
        {:else}
          <tr><td colspan="5" class="muted">Brak wpisów.</td></tr>
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
