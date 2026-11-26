<script>
  import { api } from '../lib/api.js'
  import { t } from '../i18n/i18n.svelte.js'

  let lines = $state([])
  let error = $state('')
  function load() {
    api.logs().then((l) => (lines = l || []), (e) => (error = e.message))
  }
  load()
</script>

<div class="card">
  <h2>{t('logs.title')} <span class="muted small">{t('logs.note')}</span>
    <button class="refresh" onclick={load}>{t('common.refresh')}</button></h2>
  {#if error}<div class="banner bad">{error}</div>{/if}
  <pre>{lines.join('\n') || t('logs.none')}</pre>
</div>

<style>
  h2 {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .refresh {
    margin-left: auto;
    font-size: 13px;
  }
  pre {
    margin: 0;
    font-size: 12px;
    max-height: 70vh;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-all;
  }
</style>
