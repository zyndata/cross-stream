<script>
  import { onMount } from 'svelte'
  import { api, errorText } from '../lib/api.js'

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
      notify('Zapisano')
    } catch (e) {
      notify(errorText(e), true)
    }
  }
</script>

{#if pref}
  <div class="card">
    <h2>Zamykanie okna</h2>
    <p class="muted small">Co zrobić z hubem (usługa xs-hubd), gdy zamykasz to okno.</p>
    {#each [['ask', 'Pytaj za każdym razem'], ['keep', 'Zostaw hub w tle'], ['stop', 'Zatrzymaj hub (hotspot się wyłączy)']] as [v, label]}
      <label class="check">
        <input type="radio" name="onclose" checked={pref === v} onchange={() => save(v)} />
        {label}
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
