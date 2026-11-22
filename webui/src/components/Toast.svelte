<script>
  let { message, bad = false } = $props()
</script>

{#if message}
  <div class="toast" class:bad role="status">{message}</div>
{/if}

<style>
  .toast {
    position: fixed;
    bottom: 16px;
    right: 16px;
    background: var(--panel);
    border: 1px solid var(--line);
    border-left: 4px solid var(--ok);
    border-radius: 8px;
    padding: 10px 14px;
    box-shadow: 0 4px 20px rgb(0 0 0 / 20%);
    z-index: 20;
    max-width: calc(100vw - 32px);
  }
  .toast.bad {
    border-left-color: var(--bad);
  }
</style>
