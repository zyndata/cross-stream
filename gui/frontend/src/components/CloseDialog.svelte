<script>
  // Asked on window close while xs-hubd runs (phase 12): keep the hub
  // running in the background, stop it and close, or cancel.
  import { api, errorText } from '../lib/api.js'
  import { t } from '../i18n/i18n.svelte.js'

  let { onclose, notify } = $props()
  let remember = $state(false)
  let busy = $state(false)
  let keepButton = $state()

  $effect(() => {
    keepButton?.focus()
  })

  async function choose(c) {
    busy = true
    try {
      await api.closeChoice(c, remember)
    } catch (e) {
      busy = false
      notify(errorText(e), true)
    }
  }
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && !busy && onclose()} />

<div class="backdrop" role="presentation" onclick={(e) => e.target === e.currentTarget && !busy && onclose()}>
  <div class="dialog" role="alertdialog" aria-modal="true" aria-labelledby="close-title">
    <h3 id="close-title">{t('closeDlg.title')}</h3>
    <p>{t('closeDlg.body')}</p>
    <p class="warn">{t('closeDlg.warn')}</p>
    <label class="check">
      <input type="checkbox" bind:checked={remember} disabled={busy} />
      {t('closeDlg.remember')}
    </label>
    <div class="buttons">
      <button onclick={onclose} disabled={busy}>{t('common.cancel')}</button>
      <button class="danger-fill" onclick={() => choose('stop')} disabled={busy}>{t('closeDlg.stop')}</button>
      <button bind:this={keepButton} class="primary" onclick={() => choose('keep')} disabled={busy}>{t('close.pref.keep')}</button>
    </div>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgb(0 0 0 / 45%);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 20;
  }
  .dialog {
    background: var(--panel);
    border: 1px solid var(--line);
    border-radius: 12px;
    padding: 20px 22px 16px;
    width: min(520px, calc(100vw - 32px));
    box-shadow: 0 10px 40px rgb(0 0 0 / 35%);
  }
  h3 {
    margin: 0 0 10px;
    font-size: 16px;
  }
  p {
    margin: 0 0 8px;
  }
  .warn {
    color: var(--warn);
  }
  .check {
    display: flex;
    gap: 8px;
    align-items: center;
    margin-top: 6px;
  }
  .buttons {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 18px;
    flex-wrap: wrap;
  }
  .danger-fill {
    background: var(--bad);
    border-color: var(--bad);
    color: #fff;
  }
</style>
