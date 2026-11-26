<script>
  // Shown at GUI start while the hub is not enrolled (and from the banner):
  // step 1 adds a phone (pairing QR), step 2 takes the XS1 code. With a
  // phone connected the enrollment goes through it; without one, through a
  // local link or this computer's own connection (phase 19, owner decision).
  import { api, enrollErrorText } from '../lib/api.js'
  import Pairing from './Pairing.svelte'
  import { t } from '../i18n/i18n.svelte.js'

  let { view, notify, onclose } = $props()
  let phones = $derived((view.relays ?? []).filter((r) => r.connected))
  let links = $derived((view.uplinks ?? []).filter((u) => u.enabled).length)
  let step = $state(1)
  let code = $state('')
  let busy = $state(false)
  let err = $state('')
  let area = $state()
  let started = false

  $effect(() => {
    if (!started) {
      started = true
      if (phones.length > 0) step = 2 // a phone is already there
    }
  })
  $effect(() => {
    if (step === 2) area?.focus()
  })

  async function enroll() {
    busy = true
    err = ''
    try {
      const e = await api.enroll(code.trim())
      notify(t('enroll.doneAsToast', { name: e.name }))
      onclose()
    } catch (e) {
      err = enrollErrorText(e)
    } finally {
      busy = false
    }
  }
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && !busy && onclose()} />

<div class="backdrop" role="presentation">
  <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="enr-title">
    <h3 id="enr-title">{t('enroll.dialogTitle')} <span class="muted">· {t('enroll.step', { n: step, total: 2 })}</span></h3>
    {#if step === 1}
      <p class="small">
        {t('enroll.step1')}
      </p>
      <Pairing {notify} />
      {#if phones.length > 0}
        <p class="ok small">{t('enroll.phoneConnected', { names: phones.map((r) => r.name).join(', ') })}</p>
      {:else}
        <p class="muted small">{t('enroll.waitPhone')}</p>
      {/if}
      <div class="buttons">
        <button type="button" onclick={onclose}>{t('enroll.later')}</button>
        {#if phones.length === 0}
          <button type="button" onclick={() => (step = 2)}>{t('enroll.noPhone')}</button>
        {/if}
        <button type="button" class="primary" disabled={phones.length === 0} onclick={() => (step = 2)}>{t('enroll.next')}</button>
      </div>
    {:else}
      <form onsubmit={(e) => (e.preventDefault(), enroll())}>
        <label>
          {t('server.codeLabel')}
          <textarea bind:this={area} bind:value={code} rows="3" placeholder="XS1.…" spellcheck="false"></textarea>
        </label>
        <p class="muted small">
          {#if phones.length > 0}
            {t('enroll.viaPhone', { names: phones.map((r) => r.name).join(', ') })}
          {:else if links > 0}
            {t('enroll.viaLink')}
          {:else}
            {t('enroll.viaComputer')}
          {/if}
        </p>
        {#if err}<p class="bad small">{err}</p>{/if}
        <div class="buttons">
          <button type="button" onclick={() => ((step = 1), (err = ''))} disabled={busy}>{t('enroll.back')}</button>
          <button type="button" onclick={onclose} disabled={busy}>{t('enroll.later')}</button>
          <button class="primary" disabled={busy || !code.trim().startsWith('XS1.')}>
            {busy ? t('enroll.busy') : t('enroll.button')}
          </button>
        </div>
      </form>
    {/if}
  </div>
</div>

<style>
  .ok {
    color: var(--ok);
  }
  .bad {
    color: var(--bad);
  }
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgb(0 0 0 / 45%);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 10;
  }
  .dialog :global(.card) {
    box-shadow: none;
    margin: 8px 0;
  }
  .dialog {
    background: var(--panel);
    border: 1px solid var(--line);
    border-radius: 12px;
    padding: 20px 22px 16px;
    width: min(520px, calc(100vw - 32px));
    max-height: calc(100vh - 32px);
    overflow: auto;
    box-shadow: 0 10px 40px rgb(0 0 0 / 35%);
  }
  h3 {
    margin: 0 0 10px;
    font-size: 16px;
  }
  textarea {
    width: 100%;
    margin-top: 6px;
  }
  .buttons {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 14px;
  }
</style>
