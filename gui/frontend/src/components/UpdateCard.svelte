<script>
  // Settings: version, check at start on/off, check now, install.
  import { api, errorText } from '../lib/api.js'
  import { t } from '../i18n/i18n.svelte.js'
  import { upd } from '../lib/update.svelte.js'

  let { notify } = $props()
  const v = $derived(upd.v)
  const busy = $derived(v && v.state !== 'idle')

  async function check() {
    try {
      upd.v = await api.checkUpdate()
    } catch (e) {
      notify(errorText(e), true)
    }
  }

  async function setAuto(on) {
    try {
      await api.setUpdateAuto(on)
      upd.v = { ...upd.v, auto: on }
    } catch (e) {
      notify(errorText(e), true)
    }
  }
</script>

{#if v}
  <div class="card">
    <h2>{t('upd.title')}</h2>
    <p class="muted small">
      {t('upd.version')} <b>{v.version}</b>. {t('upd.hint')}
    </p>
    {#if !v.release}
      <p class="muted small">{t('upd.dev')}</p>
    {/if}
    <label class="check">
      <input type="checkbox" checked={v.auto} onchange={(e) => setAuto(e.currentTarget.checked)} />
      {t('upd.auto')}
    </label>
    <div class="row">
      <button onclick={check} disabled={busy}>{v.state === 'checking' ? t('uplink.checking') : t('upd.checkNow')}</button>
      {#if v.available}
        <span>{t('upd.available')} <b>{v.available.tag}</b>.</span>
        {#if v.state === 'downloading'}
          <span class="muted">{t('upd.downloadingShort')} {v.progress}%</span>
        {:else if v.state === 'installing'}
          <span class="muted">{t('upd.installing')}</span>
        {:else}
          {#if v.can_install}<button class="primary" onclick={() => (upd.confirm = true)}>{t('upd.install')}</button>{/if}
          <button onclick={() => api.openURL(v.available.page)}>{t('upd.whatsNew')}</button>
        {/if}
      {:else if v.checked}
        <span class="muted">{t('upd.latest', { tag: v.latest })}{v.release ? t('upd.upToDate') : '.'}</span>
      {/if}
    </div>
    {#if v.available && !v.can_install && v.release}
      <p class="muted small">
        {t('upd.cannotInstall')} <code>sudo xs-hubctl update -install</code>.
      </p>
    {/if}
    {#if v.error}<p class="err small">{v.error}</p>{/if}
  </div>
{/if}

<style>
  .check {
    display: flex;
    gap: 8px;
    align-items: center;
    margin: 6px 0;
  }
  .row {
    display: flex;
    gap: 10px;
    align-items: center;
    flex-wrap: wrap;
    margin-top: 6px;
  }
  .err {
    color: var(--bad);
  }
</style>
