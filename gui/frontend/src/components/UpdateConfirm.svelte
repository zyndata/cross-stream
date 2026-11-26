<script>
  // Confirmation before installing an update: data cost, password, hub restart.
  import { onMount } from 'svelte'
  import { api, errorText } from '../lib/api.js'
  import { upd, sizeMB } from '../lib/update.svelte.js'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import { t } from '../i18n/i18n.svelte.js'

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
  <ConfirmDialog title={t('upd.ask', { tag: v.available.tag })} yes={t('upd.install')} no={t('common.cancel')}
    onyes={install} onno={() => (upd.confirm = false)}>
    <ul>
      <li>{t('upd.ask1', { mb: sizeMB(v.available.size) })}</li>
      <li>{windows ? t('upd.askUac') : t('upd.askPassword')}</li>
      <li>{t('upd.ask3')}</li>
      <li>{t('upd.ask4')}</li>
    </ul>
  </ConfirmDialog>
{/if}
