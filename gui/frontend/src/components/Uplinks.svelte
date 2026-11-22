<script>
  import UplinkRow from './UplinkRow.svelte'

  let { view, notify } = $props()
  const links = $derived(view.uplinks ?? [])
</script>

<div class="card">
  <h2>Łącza lokalne ({links.filter((u) => u.state === 'up').length} z {links.length} aktywnych)</h2>
  {#if links.length === 0}
    <p class="muted">
      Brak łączy laptopa z internetem. Podłącz telefon kablem USB i włącz w nim „Tethering USB” (bez aplikacji,
      działa też iPhone), podłącz Ethernet, modem USB albo użyj karty SIM w laptopie — pojawią się tutaj.
    </p>
  {:else}
    <p class="muted small">
      Nowe łącze jest wyłączone, dopóki go nie włączysz — dane z modemu czy telefonu nie są używane bez zgody.
    </p>
    <div class="scroll">
      <table>
        <thead>
          <tr>
            <th>Używaj</th>
            <th>Łącze</th>
            <th>Stan</th>
            <th class="num">↑ / ↓</th>
            <th class="num">RTT · straty</th>
            <th class="num">Zużyte</th>
            <th>Priorytet</th>
            <th>Wysyłanie</th>
            <th>Pobieranie</th>
            <th title="Limit prędkości, puste = bez limitu">Limit kb/s</th>
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
