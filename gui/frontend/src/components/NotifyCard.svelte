<script>
  import { onMount } from 'svelte'
  import { api, errorText } from '../lib/api.js'

  let { notify } = $props()
  let prefs = $state(null)

  const labels = [
    ['relay_left', 'Telefon lub łącze lokalne się odłączyło'],
    ['relay_joined', 'Telefon lub łącze lokalne dołączyło'],
    ['cap_80', 'Limit danych telefonu w 80%'],
    ['cap_100', 'Limit danych telefonu wyczerpany'],
    ['battery_low', 'Słaba bateria telefonu (poniżej 25%)'],
    ['weak_wifi', 'Słaby sygnał Wi-Fi telefonu'],
    ['mode_changed', 'Zmiana trybu huba'],
    ['speedtest_done', 'Koniec testu łączy'],
  ]

  onMount(() => api.notifyPrefs().then((p) => (prefs = p)).catch((e) => notify(errorText(e), true)))

  async function toggle(k, on) {
    prefs = { ...prefs, [k]: on }
    try {
      await api.setNotifyPrefs(prefs)
    } catch (e) {
      notify(errorText(e), true)
    }
  }
</script>

<div class="card">
  <h2>Powiadomienia</h2>
  <p class="muted small">
    Powiadomienia systemowe; to samo zdarzenie z tego samego telefonu najwyżej raz na 5 minut. Pokazuje je to
    okno: gdy jest zamknięte (hub działa w tle), powiadomień nie ma.
  </p>
  {#if prefs}
    {#each labels as [k, label]}
      <label class="check">
        <input type="checkbox" checked={prefs[k]} onchange={(e) => toggle(k, e.currentTarget.checked)} />
        {label}
      </label>
    {/each}
  {/if}
</div>

<style>
  .check {
    display: flex;
    gap: 8px;
    align-items: center;
    margin: 6px 0;
  }
</style>
