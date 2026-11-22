<script>
  import { api } from '../lib/api.js'

  let lines = $state([])
  let error = $state('')
  function load() {
    api.logs().then((l) => (lines = l || []), (e) => (error = e.message))
  }
  load()
</script>

<div class="card">
  <h2>Logi serwera <span class="muted small">(ostatnie 200 linii, zanonimizowane)</span>
    <button class="refresh" onclick={load}>Odśwież</button></h2>
  {#if error}<div class="banner bad">{error}</div>{/if}
  <pre>{lines.join('\n') || 'Brak linii.'}</pre>
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
