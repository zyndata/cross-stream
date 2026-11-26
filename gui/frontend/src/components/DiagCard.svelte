<script>
  import { api, errorText } from '../lib/api.js'
  import { t, loc } from '../i18n/i18n.svelte.js'

  let { view, notify } = $props()
  let busy = $state(false)
  let switching = $state(false)

  // Debug logging from the hub status; null with an older xs-hubd (switch hidden).
  let lvl = $derived(view?.log_level ?? null)
  let verbose = $derived(lvl?.level === 'debug')
  let until = $derived(
    lvl?.until ? new Date(lvl.until).toLocaleTimeString(loc(), { hour: '2-digit', minute: '2-digit' }) : '',
  )

  async function exportLogs() {
    busy = true
    try {
      const path = await api.exportLogs()
      if (path) notify(t('diag.saved', { path }))
    } catch (e) {
      notify(errorText(e), true)
    } finally {
      busy = false
    }
  }

  async function setVerbose(el) {
    const on = el.checked
    switching = true
    try {
      await api.setVerboseLogs(on)
      notify(on ? t('diag.verboseOn') : t('diag.verboseOff'))
    } catch (e) {
      el.checked = !on // bound one way: put back what the hub still has
      notify(errorText(e), true)
    } finally {
      switching = false
    }
  }
</script>

<div class="card">
  <h2>{t('diag.title')}</h2>
  <p class="muted small">
    {t('diag.hint')}
  </p>
  <div class="row">
    <button onclick={exportLogs} disabled={busy}>{busy ? t('diag.saving') : t('diag.export')}</button>
    {#if lvl}
      <label class="switch" title={t('diag.verboseHint')}>
        <input type="checkbox" checked={verbose} disabled={switching}
          onchange={(e) => setVerbose(e.currentTarget)} />
        <span>{t('diag.verbose')}</span>
      </label>
      {#if verbose && until}<span class="muted small">{t('diag.until', { time: until })}</span>{/if}
    {/if}
  </div>
</div>

<style>
  .row {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.75rem;
  }
</style>
