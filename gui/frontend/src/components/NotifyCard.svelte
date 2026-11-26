<script>
  import { onMount } from 'svelte'
  import { api, errorText } from '../lib/api.js'
  import { t } from '../i18n/i18n.svelte.js'

  let { notify } = $props()
  let prefs = $state(null)

  const types = ['relay_left', 'relay_joined', 'cap_80', 'cap_100', 'battery_low', 'weak_wifi', 'mode_changed', 'speedtest_done']

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
  <h2>{t('notify.title')}</h2>
  <p class="muted small">
    {t('notify.hint')}
  </p>
  {#if prefs}
    {#each types as k}
      <label class="check">
        <input type="checkbox" checked={prefs[k]} onchange={(e) => toggle(k, e.currentTarget.checked)} />
        {t('notify.type.' + k)}
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
