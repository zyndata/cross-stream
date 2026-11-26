<script>
  // "New version available" bar above every view (also when xs-hubd is not running).
  import { api, errorText } from '../lib/api.js'
  import { t } from '../i18n/i18n.svelte.js'
  import { upd } from '../lib/update.svelte.js'

  let { notify } = $props()
  const v = $derived(upd.v)
  const busy = $derived(v?.state === 'downloading' || v?.state === 'installing')
  const show = $derived(!!v?.available && (!v.dismissed || busy))

  async function dismiss() {
    try {
      await api.dismissUpdate()
    } catch (e) {
      notify(errorText(e), true)
    }
  }
</script>

{#if show}
  <div class="banner info">
    {#if v.state === 'downloading'}
      {t('upd.downloading', { tag: v.available.tag })} <b>{v.progress}%</b>
    {:else if v.state === 'installing'}
      {t('upd.installingBanner', { tag: v.available.tag })}
    {:else}
      <span class="text">
        <b>{t('upd.newVersion', { tag: v.available.tag })}</b> {t('upd.youHave', { version: v.version })}
        {#if v.error}<span class="err">{v.error}</span>{/if}
      </span>
      <span class="actions">
        {#if v.can_install}<button class="primary" onclick={() => (upd.confirm = true)}>{t('upd.install')}</button>{/if}
        <button onclick={() => api.openURL(v.available.page)}>{t('upd.whatsNew')}</button>
        <button class="x" title={t('upd.dismiss')} aria-label={t('common.close')} onclick={dismiss}>×</button>
      </span>
    {/if}
  </div>
{/if}

<style>
  .info {
    display: flex;
    align-items: center;
    gap: 12px;
    background: color-mix(in srgb, var(--accent) 14%, transparent);
    color: var(--text);
  }
  .text {
    flex: 1;
  }
  .err {
    color: var(--bad);
    margin-left: 6px;
  }
  .actions {
    display: flex;
    gap: 6px;
    margin-left: auto;
  }
  .x {
    padding: 2px 10px;
  }
</style>
