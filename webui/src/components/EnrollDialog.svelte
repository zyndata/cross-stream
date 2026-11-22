<script>
  // Enroll a new hub: the XS1 code is shown once (it is not stored in
  // readable form on the server), with a copy button and its expiry.
  import { api } from '../lib/api.js'
  import { when } from '../lib/format.js'

  let { onclose, ondone } = $props()
  let name = $state('')
  let result = $state(null)
  let error = $state('')
  let copied = $state(false)
  let busy = $state(false)

  async function create(e) {
    e.preventDefault()
    busy = true
    error = ''
    try {
      result = await api.enroll(name.trim())
      ondone?.()
    } catch (err) {
      error = err.message
    } finally {
      busy = false
    }
  }

  let codeArea = $state(null)

  // The Clipboard API needs a secure context and, inside the Home Assistant
  // ingress iframe, a clipboard-write permission the iframe does not grant:
  // fall back to selecting the textarea and execCommand('copy').
  async function copy() {
    error = ''
    try {
      await navigator.clipboard.writeText(result.code)
      copied = true
      return
    } catch {
      // fall through to the legacy path
    }
    codeArea?.focus()
    codeArea?.select()
    if (document.execCommand?.('copy')) {
      copied = true
    } else {
      error = 'Nie udało się skopiować — kod jest zaznaczony, naciśnij Ctrl+C.'
    }
  }
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && onclose()} />

<div class="backdrop" role="presentation" onclick={(e) => e.target === e.currentTarget && onclose()}>
  <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="enroll-title">
    <h3 id="enroll-title">Dodaj hub</h3>
    {#if !result}
      <form onsubmit={create}>
        <label>Nazwa huba <input bind:value={name} maxlength="64" required /></label>
        <p class="muted small">Serwer utworzy jednorazowy kod XS1. Wpisz go na hubie (GUI albo <code>xs-hubctl enroll</code>).</p>
        {#if error}<div class="banner bad">{error}</div>{/if}
        <div class="buttons">
          <button type="button" onclick={onclose}>Anuluj</button>
          <button class="primary" disabled={busy || !name.trim()}>Utwórz kod</button>
        </div>
      </form>
    {:else}
      <p>Kod dla huba <b>{name.trim()}</b> — pokazany tylko teraz:</p>
      <textarea bind:this={codeArea} readonly rows="4" value={result.code} onfocus={(e) => e.currentTarget.select()}></textarea>
      <p class="muted small">Ważny do {when(result.expires)}. Po zamknięciu okna kodu nie da się wyświetlić ponownie.</p>
      {#if error}<div class="banner bad">{error}</div>{/if}
      <div class="buttons">
        <button onclick={copy}>{copied ? 'Skopiowano' : 'Kopiuj'}</button>
        <button class="primary" onclick={onclose}>Zamknij</button>
      </div>
    {/if}
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
    z-index: 10;
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
  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  textarea {
    width: 100%;
    font-family: ui-monospace, monospace;
    font-size: 12px;
    word-break: break-all;
    resize: none;
  }
  .buttons {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 14px;
  }
</style>
