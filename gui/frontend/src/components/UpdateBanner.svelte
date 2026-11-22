<script>
  // "New version available" bar above every view (also when xs-hubd is not running).
  import { api, errorText } from '../lib/api.js'
  import { upd } from '../lib/update.svelte.js'

  let { notify } = $props()
  const v = $derived(upd.v)
  const busy = $derived(v?.state === 'downloading' || v?.state === 'installing')
  const show = $derived(!!v?.available && (!v.dismissed || busy))

  async function dismiss() {
    try {
      await api.dismissUpdate()
    } catch (e) {
      notify(errorText(e), true)
    }
  }
</script>

{#if show}
  <div class="banner info">
    {#if v.state === 'downloading'}
      Pobieranie CrossStream {v.available.tag}… <b>{v.progress}%</b>
    {:else if v.state === 'installing'}
      Instalowanie {v.available.tag}: potwierdź uprawnienia administratora. Hub uruchomi się ponownie, a to okno
      otworzy się w nowej wersji.
    {:else}
      <span class="text">
        <b>Dostępna nowa wersja {v.available.tag}</b> (masz {v.version}).
        {#if v.error}<span class="err">{v.error}</span>{/if}
      </span>
      <span class="actions">
        {#if v.can_install}<button class="primary" onclick={() => (upd.confirm = true)}>Zainstaluj</button>{/if}
        <button onclick={() => api.openURL(v.available.page)}>Co nowego</button>
        <button class="x" title="Nie pokazuj dla tej wersji" aria-label="Zamknij" onclick={dismiss}>×</button>
      </span>
    {/if}
  </div>
{/if}

<style>
  .info {
    display: flex;
    align-items: center;
    gap: 12px;
    background: color-mix(in srgb, var(--accent) 14%, transparent);
    color: var(--text);
  }
  .text {
    flex: 1;
  }
  .err {
    color: var(--bad);
    margin-left: 6px;
  }
  .actions {
    display: flex;
    gap: 6px;
    margin-left: auto;
  }
  .x {
    padding: 2px 10px;
  }
</style>
