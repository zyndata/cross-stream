<script>
  import { api } from '../lib/api.js'

  let { hubs = [], onchange, toast } = $props()
  let edits = $state({})

  async function save(name) {
    const n = Number(edits[name])
    try {
      await api.forward(name, n)
      toast(`${name}: można przekierować ${n} portów`)
      delete edits[name]
    } catch (err) {
      toast(err.message, true)
    }
    onchange()
  }
</script>

<div class="card">
  <h2>Przekierowania portów</h2>
  <p class="muted small">
    Ile publicznych portów (1024–49151) hub może przekierować do swojej sieci. Hub sam wybiera porty
    (GUI huba); tu widać aktywne przekierowania.
  </p>
  {#if hubs.length === 0}
    <p class="muted">Brak hubów.</p>
  {:else}
    <div class="scroll">
      <table>
        <thead><tr><th>Hub</th><th>Limit (0–20)</th><th>Aktywne</th></tr></thead>
        <tbody>
          {#each hubs as h (h.name)}
            <tr>
              <td>{h.name}</td>
              <td class="allow">
                <input type="number" min="0" max="20" value={edits[h.name] ?? h.fwd_allow}
                  oninput={(e) => (edits[h.name] = e.currentTarget.value)} />
                {#if edits[h.name] !== undefined && edits[h.name] !== '' && Number.isInteger(Number(edits[h.name])) && Number(edits[h.name]) !== h.fwd_allow}
                  <button class="primary" onclick={() => save(h.name)}>Zapisz</button>
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
