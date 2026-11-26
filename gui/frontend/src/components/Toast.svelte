<script>
  import { t } from '../i18n/i18n.svelte.js'

  let { toast } = $props()
  let shown = $state(null)
  let timer

  $effect(() => {
    if (!toast) return
    shown = toast
    clearTimeout(timer)
    timer = setTimeout(() => (shown = null), toast.bad ? 8000 : 3500)
  })
</script>

{#if shown}
  <div class="toast" class:bad={shown.bad} role="status">
    {shown.text}
    <button aria-label={t('common.close')} onclick={() => (shown = null)}>×</button>
  </div>
{/if}

<style>
  .toast {
    position: fixed;
    bottom: 18px;
    left: 50%;
    transform: translateX(-50%);
    background: var(--text);
    color: var(--bg);
    padding: 10px 14px;
    border-radius: 8px;
    max-width: 80vw;
    display: flex;
    gap: 12px;
    align-items: center;
    box-shadow: 0 4px 16px rgb(0 0 0 / 25%);
  }
  .toast.bad {
    background: var(--bad);
    color: #fff;
  }
  button {
    background: transparent;
    border: none;
    color: inherit;
    font-size: 18px;
    padding: 0 4px;
  }
</style>
