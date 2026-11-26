<script>
  import { onMount } from 'svelte'
  import { api, errorText } from '../lib/api.js'
  import { t } from '../i18n/i18n.svelte.js'

  let { notify } = $props()
  let info = $state(null)
  let text = $state('')
  let busy = $state(false)

  function fill(i) {
    info = i
    text = [...(i.domains ?? []), ...(i.cidrs ?? [])].join('\n')
  }
  onMount(() => api.bypass().then(fill).catch((e) => notify(errorText(e), true)))

  // An entry that looks like an IPv4 address or CIDR is a CIDR, anything else a domain.
  const isCIDR = (s) => /^\d{1,3}(\.\d{1,3}){3}(\/\d{1,2})?$/.test(s)

  async function save() {
    const items = text.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean)
    busy = true
    try {
      fill(await api.setBypass(items.filter((s) => !isCIDR(s)), items.filter(isCIDR)))
      notify(t('bypass.saved'))
    } catch (e) {
      notify(errorText(e), true)
    } finally {
      busy = false
    }
  }
</script>

<div class="card">
  <h2>{t('bypass.title')}</h2>
  <p class="small">
    {t('bypass.hint')}
  </p>
  <div class="banner warn small">
    {t('bypass.warn')}
  </div>
  <form onsubmit={(e) => (e.preventDefault(), save())}>
    <label>
      {t('bypass.label')}
      <textarea bind:value={text} rows="5" spellcheck="false" placeholder="bank.pl"></textarea>
    </label>
    {#if info && (info.domains?.length || info.cidrs?.length)}
      <p class="muted small">
        {t('bypass.stats', { learned: info.learned, relays: info.relays })}
        {#if info.relays === 0}<b> — {t('bypass.noRelays')}</b>{/if}
      </p>
    {/if}
    <button class="primary" disabled={busy}>{busy ? t('diag.saving') : t('common.save')}</button>
  </form>
</div>

<style>
  form {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 13px;
    color: var(--muted);
  }
  textarea {
    color: var(--text);
    font-family: ui-monospace, monospace;
    font-size: 12px;
  }
  .banner {
    margin: 8px 0;
  }
</style>
