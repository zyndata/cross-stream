<script>
  import ConfirmDialog from './ConfirmDialog.svelte'
  import EnrollDialog from './EnrollDialog.svelte'
  import RenameDialog from './RenameDialog.svelte'
  import { api } from '../lib/api.js'
  import { mbps, when } from '../lib/format.js'

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
      toast(`Hub ${name} usunięty`)
    } catch (err) {
      toast(err.message, true)
    }
    onchange()
  }

  async function rename(name, newName) {
    renaming = null
    try {
      await api.rename(name, newName)
      toast(`Zmieniono nazwę: ${name} → ${newName}`)
    } catch (err) {
      toast(err.message, true)
    }
    onchange()
  }
</script>

<div class="card">
  <h2>
    Huby
    <button class="primary add" onclick={() => (enrolling = true)}>Dodaj hub</button>
  </h2>
  {#if stale.length}
    <div class="banner bad">
      Port tunelu serwera się zmienił. Te huby mają w kodzie stary port i nie połączą się, dopóki nie zarejestrujesz
      ich ponownie (Usuń → Dodaj hub → nowy kod w hubie): {stale.map((h) => h.name).join(', ')}
    </div>
  {/if}
  {#if hubs.length === 0}
    <p class="muted">Brak zarejestrowanych hubów. Kliknij „Dodaj hub”, aby utworzyć kod rejestracji.</p>
  {:else}
    <div class="scroll">
      <table>
        <thead>
          <tr><th>Nazwa</th><th>Stan</th><th>Adres w tunelu</th><th>Ścieżki</th><th>Od huba</th><th>Do huba</th><th>Dodany</th><th></th></tr>
        </thead>
        <tbody>
          {#each hubs as h (h.name)}
            {@const s = byHub[h.name]}
            <tr>
              <td><a href="#/hubs/{encodeURIComponent(h.name)}">{h.name}</a></td>
              <td>
                <span class="chip" class:ok={h.online}>{h.online ? 'online' : 'offline'}</span>
                {#if h.reenroll}<span class="chip bad" title="Zarejestrowany z portem {h.port}">zarejestruj ponownie</span>{/if}
              </td>
              <td>{h.tunnel_ip}</td>
              <td>{s ? s.paths?.length || 0 : '—'}</td>
              <td>{s ? mbps(s.rx_rate) : '—'}</td>
              <td>{s ? mbps(s.tx_rate) : '—'}</td>
              <td class="small">{when(h.enrolled)}</td>
              <td class="actions">
                <button onclick={() => (renaming = h.name)}>Zmień nazwę</button>
                <button class="danger" onclick={() => (revoking = h.name)}>Usuń</button>
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
  <ConfirmDialog title="Usunąć hub?" yes="Usuń" no="Anuluj" danger onyes={() => revoke(revoking)} onno={() => (revoking = null)}>
    Hub <b>{revoking}</b> straci dostęp od razu: jego klucz zostanie usunięty, a sesja zamknięta. Aby go znów
    podłączyć, trzeba go zarejestrować od nowa.
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
