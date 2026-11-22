<script>
  import { mbps, bytes, ms, pct, ago, pathState, priority } from '../lib/format.js'

  let { name, hubs = [], sessions = [] } = $props()
  let hub = $derived(hubs.find((h) => h.name === name))
  let s = $derived(sessions.find((x) => x.hub === name))
</script>

<p><a href="#/hubs">← Huby</a></p>

{#if !hub}
  <div class="card"><p class="muted">Nie ma huba „{name}”.</p></div>
{:else}
  <div class="card">
    <h2>{hub.name} <span class="chip" class:ok={hub.online}>{hub.online ? 'online' : 'offline'}</span></h2>
    <dl>
      <dt>Adres w tunelu</dt><dd>{hub.tunnel_ip}</dd>
      <dt>Klucz publiczny</dt><dd><code>{hub.public_key}</code></dd>
      {#if s}
        <dt>Od huba / do huba</dt><dd>{mbps(s.rx_rate)} / {mbps(s.tx_rate)}</dd>
        <dt>Dane od / do huba</dt><dd>{bytes(s.rx_bytes)} / {bytes(s.tx_bytes)}</dd>
        <dt>Tryb</dt><dd>{s.bond.mode || '—'}, nadmiarowość {s.bond.redundancy || '—'}{s.bond.redundant ? ' (aktywna)' : ''}</dd>
        <dt>Kolejka / zgubione / pominięte</dt><dd>{s.bond.queue} / {s.bond.dropped} / {s.bond.skipped}</dd>
        <dt>Ostatni pakiet</dt><dd>{ago(s.last_seen)}</dd>
      {/if}
    </dl>
  </div>

  <div class="card">
    <h2>Ścieżki (na żywo)</h2>
    {#if !s || !s.paths?.length}
      <p class="muted">Hub nie ma teraz żadnej ścieżki.</p>
    {:else}
      <div class="scroll">
        <table>
          <thead>
            <tr>
              <th>Relay</th><th>Adres widziany</th><th>Transport</th><th>Stan</th><th>Priorytet</th>
              <th>RTT</th><th>Jitter</th><th>Straty</th><th>Do huba</th><th>Od huba</th><th>MTU</th>
            </tr>
          </thead>
          <tbody>
            {#each s.paths as p (p.id)}
              <tr>
                <td>{p.name || `ścieżka ${p.id}`}</td>
                <td>{p.addr}</td>
                <td>{p.stats.stream ? 'TCP/WSS' : 'UDP'}</td>
                <td><span class="chip" class:ok={p.stats.state === 'alive'} class:bad={p.stats.state === 'dead'}>{pathState(p.stats.state)}</span></td>
                <td>{priority(p.stats.priority)}</td>
                <td>{ms(p.stats.srtt)}</td>
                <td>{ms(p.stats.jitter)}</td>
                <td>{pct(p.stats.loss)}</td>
                <td>{mbps(p.stats.tx_rate)}</td>
                <td>{mbps(p.stats.rx_rate)}</td>
                <td>{p.stats.mtu || '—'}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </div>
{/if}

<style>
  dl {
    display: grid;
    grid-template-columns: max-content 1fr;
    gap: 4px 16px;
    margin: 0;
  }
  dt {
    color: var(--muted);
  }
  dd {
    margin: 0;
    word-break: break-all;
  }
  .chip.ok {
    color: var(--ok);
  }
</style>
