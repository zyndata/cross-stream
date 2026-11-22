<script>
  import { untrack } from 'svelte'
  import ConfirmDialog from './ConfirmDialog.svelte'

  let { name, onrename, onclose } = $props()
  let newName = $state(untrack(() => name)) // the dialog edits a copy
</script>

<ConfirmDialog title="Zmień nazwę huba" yes="Zmień" no="Anuluj" onyes={() => newName.trim() && onrename(newName.trim())} onno={onclose}>
  <label>
    Nowa nazwa dla <b>{name}</b>
    <input bind:value={newName} maxlength="64" />
  </label>
  <p class="muted small">Hub połączony pod starą nazwą rozłączy się i od razu połączy pod nową (klucz i adres w tunelu zostają).</p>
</ConfirmDialog>

<style>
  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
</style>
