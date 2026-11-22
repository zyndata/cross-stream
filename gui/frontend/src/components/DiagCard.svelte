<script>
  import { api, errorText } from '../lib/api.js'

  let { view, notify } = $props()
  let busy = $state(false)
  let switching = $state(false)

  // Debug logging from the hub status; null with an older xs-hubd (switch hidden).
  let lvl = $derived(view?.log_level ?? null)
  let verbose = $derived(lvl?.level === 'debug')
  let until = $derived(
    lvl?.until ? new Date(lvl.until).toLocaleTimeString('pl-PL', { hour: '2-digit', minute: '2-digit' }) : '',
  )

  async function exportLogs() {
    busy = true
    try {
      const path = await api.exportLogs()
      if (path) notify('Zapisano logi: ' + path)
    } catch (e) {
      notify(errorText(e), true)
    } finally {
      busy = false
    }
  }

  async function setVerbose(el) {
    const on = el.checked
    switching = true
    try {
      await api.setVerboseLogs(on)
      notify(on ? 'Szczegółowe logi włączone na 15 min' : 'Szczegółowe logi wyłączone')
    } catch (e) {
      el.checked = !on // bound one way: put back what the hub still has
      notify(errorText(e), true)
    } finally {
      switching = false
    }
  }
</script>

<div class="card">
  <h2>Diagnostyka</h2>
  <p class="muted small">
    Ostatnie logi huba do pliku tekstowego. Kody, hasła, klucze, adresy MAC i publiczne adresy IP są ukryte,
    więc plik można wysłać, prosząc o pomoc.
  </p>
  <div class="row">
    <button onclick={exportLogs} disabled={busy}>{busy ? 'Zapisuję…' : 'Eksportuj logi'}</button>
    {#if lvl}
      <label class="switch" title="Więcej szczegółów w logach przez 15 minut, potem powrót do zwykłych">
        <input type="checkbox" checked={verbose} disabled={switching}
          onchange={(e) => setVerbose(e.currentTarget)} />
        <span>Szczegółowe logi (15 min)</span>
      </label>
      {#if verbose && until}<span class="muted small">do {until}</span>{/if}
    {/if}
  </div>
</div>

<style>
  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.75rem;
  }
</style>
