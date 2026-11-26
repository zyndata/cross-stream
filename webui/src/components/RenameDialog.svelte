<script>
  import { untrack } from 'svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'
  import { t } from '../i18n/i18n.svelte.js'

  let { name, onrename, onclose } = $props()
  let newName = $state(untrack(() => name)) // the dialog edits a copy
</script>

<ConfirmDialog title={t('rename.title')} yes={t('rename.yes')} no={t('common.cancel')} onyes={() => newName.trim() && onrename(newName.trim())} onno={onclose}>
  <label>
    {t('rename.label')} <b>{name}</b>
    <input bind:value={newName} maxlength="64" />
  </label>
  <p class="muted small">{t('rename.hint')}</p>
</ConfirmDialog>

<style>
  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
</style>
