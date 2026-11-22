<script>
  // Modal yes/no dialog. Esc or a click outside = "no".
  import { portal } from '../lib/portal.js'

  let { title, yes = 'Tak', no = 'Nie', danger = false, onyes, onno, children } = $props()
  let noButton = $state()

  $effect(() => {
    noButton?.focus()
  })
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && onno()} />

<div class="backdrop" use:portal role="presentation" onclick={(e) => e.target === e.currentTarget && onno()}>
  <div class="dialog" role="alertdialog" aria-modal="true" aria-labelledby="dlg-title">
    <h3 id="dlg-title">{title}</h3>
    <div class="body">{@render children()}</div>
    <div class="buttons">
      <button bind:this={noButton} onclick={onno}>{no}</button>
      <button class:primary={!danger} class:danger-fill={danger} onclick={onyes}>{yes}</button>
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
    z-index: 10;
  }
  .dialog {
    background: var(--panel);
    border: 1px solid var(--line);
    border-radius: 12px;
    padding: 20px 22px 16px;
    width: min(480px, calc(100vw - 32px));
    box-shadow: 0 10px 40px rgb(0 0 0 / 35%);
  }
  h3 {
    margin: 0 0 10px;
    font-size: 16px;
  }
  .body {
    color: var(--text);
  }
  .buttons {
    display: flex;
    justify-content: flex-end;
    gap: 8px;
    margin-top: 18px;
  }
  .danger-fill {
    background: var(--bad);
    border-color: var(--bad);
    color: #fff;
  }
</style>
