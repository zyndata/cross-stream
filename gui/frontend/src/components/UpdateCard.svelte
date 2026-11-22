<script>
  // Settings: version, check at start on/off, check now, install.
  import { api, errorText } from '../lib/api.js'
  import { upd } from '../lib/update.svelte.js'

  let { notify } = $props()
  const v = $derived(upd.v)
  const busy = $derived(v && v.state !== 'idle')

  async function check() {
    try {
      upd.v = await api.checkUpdate()
    } catch (e) {
      notify(errorText(e), true)
    }
  }

  async function setAuto(on) {
    try {
      await api.setUpdateAuto(on)
      upd.v = { ...upd.v, auto: on }
    } catch (e) {
      notify(errorText(e), true)
    }
  }
</script>

{#if v}
  <div class="card">
    <h2>Aktualizacje</h2>
    <p class="muted small">
      Wersja <b>{v.version}</b>. Sprawdzenie to jedno zapytanie do GitHuba (kilka KB), bez żadnych danych o hubie.
      Nowa wersja pobiera się dopiero po kliknięciu „Zainstaluj”.
    </p>
    {#if !v.release}
      <p class="muted small">To wersja deweloperska: nie jest aktualizowana z wydań.</p>
    {/if}
    <label class="check">
      <input type="checkbox" checked={v.auto} onchange={(e) => setAuto(e.currentTarget.checked)} />
      Sprawdzaj przy uruchomieniu aplikacji
    </label>
    <div class="row">
      <button onclick={check} disabled={busy}>{v.state === 'checking' ? 'Sprawdzam…' : 'Sprawdź teraz'}</button>
      {#if v.available}
        <span>Dostępna wersja <b>{v.available.tag}</b>.</span>
        {#if v.state === 'downloading'}
          <span class="muted">Pobieranie… {v.progress}%</span>
        {:else if v.state === 'installing'}
          <span class="muted">Instalowanie…</span>
        {:else}
          {#if v.can_install}<button class="primary" onclick={() => (upd.confirm = true)}>Zainstaluj</button>{/if}
          <button onclick={() => api.openURL(v.available.page)}>Co nowego</button>
        {/if}
      {:else if v.checked}
        <span class="muted">Najnowsze wydanie: {v.latest}{v.release ? ' — masz aktualną wersję.' : '.'}</span>
      {/if}
    </div>
    {#if v.available && !v.can_install && v.release}
      <p class="muted small">
        Ten hub nie zainstaluje aktualizacji sam (uruchomiony spoza katalogu instalacji): pobierz ją ze strony
        wydania albo użyj <code>sudo xs-hubctl update -install</code>.
      </p>
    {/if}
    {#if v.error}<p class="err small">{v.error}</p>{/if}
  </div>
{/if}

<style>
  .check {
    display: flex;
    gap: 8px;
    align-items: center;
    margin: 6px 0;
  }
  .row {
    display: flex;
    gap: 10px;
    align-items: center;
    flex-wrap: wrap;
    margin-top: 6px;
  }
  .err {
    color: var(--bad);
  }
</style>
