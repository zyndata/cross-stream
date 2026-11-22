<script>
  // Confirmation before installing an update: data cost, password, hub restart.
  import { onMount } from 'svelte'
  import { api, errorText } from '../lib/api.js'
  import { upd, sizeMB } from '../lib/update.svelte.js'
  import ConfirmDialog from './ConfirmDialog.svelte'

  let { notify } = $props()
  const v = $derived(upd.v)
  let windows = $state(false)
  onMount(async () => {
    windows = (await api.platform().catch(() => '')) === 'windows'
  })

  async function install() {
    upd.confirm = false
    try {
      await api.installUpdate()
    } catch (e) {
      notify(errorText(e), true)
    }
  }
</script>

{#if v?.available}
  <ConfirmDialog title="Zainstalować CrossStream {v.available.tag}?" yes="Zainstaluj" no="Anuluj"
    onyes={install} onno={() => (upd.confirm = false)}>
    <ul>
      <li>Pobierze ok. {sizeMB(v.available.size)} MB z GitHuba. Jeśli laptop łączy się z internetem przez telefony,
        liczy się to do ich limitu danych.</li>
      <li>{windows ? 'Windows poprosi o zgodę administratora (UAC).' : 'System poprosi o hasło administratora.'}</li>
      <li>Usługa huba uruchomi się ponownie: hotspot zniknie na kilkanaście sekund, telefony połączą się same.</li>
      <li>To okno zamknie się i otworzy w nowej wersji.</li>
    </ul>
  </ConfirmDialog>
{/if}
