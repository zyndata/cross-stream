<script>
  import { api, errorText } from '../lib/api.js'
  import RelayRow from './RelayRow.svelte'

  let { view, notify } = $props()
  let overflow = $state('')
  let editing = $state(false)

  $effect(() => {
    if (!editing) overflow = String(view.overflow_mbps ?? 0)
  })

  async function saveOverflow() {
    const v = Number(overflow)
    if (!Number.isInteger(v) || v < 0 || v > 65535) {
      notify('Próg musi być liczbą całkowitą 0–65535 Mb/s', true)
      return
    }
    try {
      await api.setOverflow(v)
      notify(v ? `Telefony „dodatkowe” włączą się powyżej ${v} Mb/s` : 'Próg wyłączony')
    } catch (e) {
      notify(errorText(e), true)
    }
    editing = false
  }
</script>

<div class="card">
  <h2>Telefony ({view.relays.filter((r) => r.connected).length} z {view.relays.length} połączonych)</h2>
  {#if view.relays.length === 0}
    <p class="muted">Brak sparowanych telefonów — zeskanuj kod QR w aplikacji CrossStream Relay.</p>
  {:else}
    <div class="scroll">
      <table>
        <thead>
          <tr>
            <th>Telefon</th>
            <th>Stan</th>
            <th title="Jak telefon łączy się z serwerem: UDP (najlepiej), TCP lub WSS, gdy operator blokuje UDP">Łącze</th>
            <th class="num">↑ / ↓</th>
            <th class="num">RTT · jitter · straty</th>
            <th class="num">Zużyte</th>
            <th>Priorytet</th>
            <th>Wysyłanie</th>
            <th>Pobieranie</th>
            <th title="Limit prędkości, puste = bez limitu">Limit kb/s</th>
            <th title="Limit danych, puste = bez limitu">Limit MB</th>
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
    <label for="overflow">Telefony „dodatkowe” używane dopiero, gdy główne przesyłają mniej niż</label>
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
    <button onclick={saveOverflow}>Zapisz</button>
    <span class="muted small">(0 = zawsze używaj wszystkich)</span>
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
