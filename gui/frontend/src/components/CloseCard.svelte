<script>
  import { onMount } from 'svelte'
  import { api, errorText } from '../lib/api.js'
  import { t } from '../i18n/i18n.svelte.js'

  let { notify } = $props()
  let pref = $state(null)

  onMount(async () => {
    try {
      if (await api.serviceControl()) pref = await api.closePref()
    } catch (e) {
      notify(errorText(e), true)
    }
  })

  async function save(v) {
    try {
      await api.setClosePref(v)
      pref = v
      notify(t('common.saved'))
    } catch (e) {
      notify(errorText(e), true)
    }
  }
</script>

{#if pref}
  <div class="card">
    <h2>{t('close.title')}</h2>
    <p class="muted small">{t('close.hint')}</p>
    {#each ['ask', 'keep', 'stop'] as v}
      <label class="check">
        <input type="radio" name="onclose" checked={pref === v} onchange={() => save(v)} />
        {t('close.pref.' + v)}
      </label>
    {/each}
  </div>
{/if}

<style>
  .check {
    display: flex;
    gap: 8px;
    align-items: center;
    margin: 6px 0;
  }
</style>
