<script>
  import { onMount } from 'svelte'
  import { api } from '../lib/api.js'
  let { view } = $props()
  // Windows: the service is CrossStreamHub (sc.exe), no sudo/systemctl.
  let windows = $state(false)
  let control = $state(false) // the GUI may start the service itself
  onMount(async () => {
    windows = (await api.platform().catch(() => '')) === 'windows'
    control = await api.serviceControl().catch(() => false)
  })
  const start = () => api.startHub().catch(() => {})
  // Wi-Fi choice: xs-hubd waits until the owner picks one (saved in the hub).
  let choosing = $state(false)
  let choiceErr = $state('')
  async function choose(choice) {
    choosing = true
    choiceErr = ''
    try {
      await api.clientWifiChoice(choice)
    } catch (e) {
      choiceErr = String(e?.message ?? e)
    } finally {
      choosing = false
    }
  }
  const triedLabel = { share: 'hotspot obok tej sieci', disconnect: 'rozłączenie Wi-Fi' }
</script>

<div class="card err">
  {#if view.state === 'wifi' && view.client_wifi}
    {@const q = view.client_wifi}
    <h2>Karta Wi-Fi jest połączona z siecią „{q.conn}”</h2>
    <p>Hub potrzebuje karty <code>{q.iface}</code> do hotspotu, z którym łączą się telefony. Wybierz, co zrobić —
      wybór zostanie zapamiętany (zmienisz go w Ustawieniach → Hotspot Wi-Fi).</p>
    {#if q.error}
      <p class="banner bad">Nie udało się: {triedLabel[q.tried] ?? q.tried}. {q.error}</p>
    {/if}
    <div class="choices">
      {#if q.can_share}
        <div>
          <button class="primary" disabled={choosing} onclick={() => choose('share')}>Hotspot obok sieci „{q.conn}”</button>
          <p class="muted small">Laptop zostaje w tej sieci, hotspot działa obok na tym samym kanale. Nie każda karta
            i nie każdy kanał na to pozwala — jeśli się nie uda, zapytam ponownie.</p>
        </div>
      {/if}
      <div>
        <button class:primary={!q.can_share} disabled={choosing} onclick={() => choose('disconnect')}>Rozłącz Wi-Fi</button>
        <p class="muted small">Laptop odłączy się od sieci „{q.conn}”; jego internet pójdzie tunelem przez telefony.</p>
      </div>
    </div>
    {#if !q.can_share}
      <p class="muted small">Ta karta nie potrafi prowadzić hotspotu obok połączenia z siecią. Inna możliwość: druga
        karta Wi-Fi (USB) na hotspot.</p>
    {/if}
    {#if choiceErr}<p class="banner bad">{choiceErr}</p>{/if}
  {:else if view.state === 'group'}
    <h2>Brak dostępu do usługi xs-hubd</h2>
    <p>Twój użytkownik nie należy do grupy <code>crossstream</code>, więc aplikacja nie może sterować hubem.</p>
    <ol>
      {#if windows}
        <li>
          Instalator (<code>install.ps1</code>) dodaje do grupy użytkownika, który go uruchomił; innego dodasz w
          PowerShell jako administrator: <code>Add-LocalGroupMember crossstream -Member NAZWA</code>
        </li>
      {:else}
        <li>Dodaj użytkownika do grupy: <code>sudo usermod -aG crossstream $USER</code></li>
      {/if}
      <li>Wyloguj się i zaloguj ponownie (albo uruchom komputer ponownie).</li>
    </ol>
  {:else if view.state === 'daemon' && view.starting}
    <h2>Uruchamiam usługę xs-hubd…</h2>
    <p>Aplikacja jest tylko panelem — hubem steruje usługa systemowa <code>xs-hubd</code>. Start trwa
      zwykle kilka–kilkanaście sekund (z hotspotem Windows ok. 10 s).</p>
  {:else if view.state === 'daemon'}
    {#if view.lost}
      <h2>Usługa xs-hubd przestała działać</h2>
      <p>Hub działał i został zatrzymany (albo proces zakończył się błędem). Telefony i klienci hotspotu
        nie mają teraz połączenia.</p>
    {:else}
      <h2>Usługa xs-hubd nie działa</h2>
      <p>Aplikacja jest tylko panelem — hubem steruje usługa systemowa <code>xs-hubd</code>.</p>
    {/if}
    {#if control}<p><button class="primary" onclick={start}>Uruchom usługę</button></p>{/if}
    {#if windows}
      <p>Aplikacja sama uruchamia usługę <code>CrossStreamHub</code>; start trwa do kilkunastu sekund.</p>
      <ol>
        <li>Jeśli to nie mija: PowerShell jako administrator, <code>Start-Service CrossStreamHub</code></li>
        <li>Logi: <code>C:\ProgramData\CrossStream\xs-hubd.log</code> (dostęp ma administrator)</li>
      </ol>
    {:else}
      <ol>
        <li>Uruchom ją: <code>sudo systemctl start xs-hubd</code></li>
        <li>Stan i logi: <code>systemctl status xs-hubd</code>, <code>journalctl -u xs-hubd -e</code></li>
      </ol>
    {/if}
  {:else}
    <h2>Błąd połączenia z xs-hubd</h2>
  {/if}
  {#if view.error}<p class="muted small">Szczegóły: {view.error}</p>{/if}
  {#if view.state !== 'wifi'}<p class="muted small">Ponawiam połączenie co 2 s…</p>{/if}
</div>

<style>
  .err {
    max-width: 720px;
    margin: 40px auto;
    padding: 22px 26px;
  }
  li {
    margin: 6px 0;
  }
  .choices {
    display: grid;
    gap: 12px;
    margin: 16px 0;
  }
  .choices p {
    margin: 4px 0 0;
  }
</style>
