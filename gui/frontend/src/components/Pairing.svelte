<script>
  // Pairing QR: refreshed when the code expires (single use, 10 min) and
  // every 30 s, since a phone that paired consumes the current code.
  import { onMount } from 'svelte'
  import { api, errorText } from '../lib/api.js'
  import { portal } from '../lib/portal.js'
  import { t } from '../i18n/i18n.svelte.js'

  let { notify } = $props()
  let qr = $state(null)
  let err = $state('')
  let now = $state(Date.now())
  let showCode = $state(false)
  let zoom = $state(false) // the QR enlarged over the window (easier to scan)

  async function load() {
    try {
      const q = await api.pairQR()
      if (!qr || q.code !== qr.code) qr = q
      err = ''
    } catch (e) {
      err = errorText(e)
    }
  }

  onMount(() => {
    load()
    const tick = setInterval(() => {
      now = Date.now()
      if (qr && new Date(qr.expires).getTime() - now < 1000) load()
    }, 1000)
    const refresh = setInterval(load, 30_000)
    return () => {
      clearInterval(tick)
      clearInterval(refresh)
    }
  })

  let left = $derived(qr ? Math.max(0, Math.round((new Date(qr.expires).getTime() - now) / 1000)) : 0)

  async function copy() {
    try {
      await navigator.clipboard.writeText(qr.code)
      notify(t('pair.copied'))
    } catch {
      showCode = true
    }
  }
</script>

<div class="card pair">
  <h2>{t('pair.title')}</h2>
  {#if qr}
    <button class="qr" onclick={() => (zoom = true)} title={t('pair.zoom')}>
      <img src="data:image/png;base64,{qr.png}" alt={t('pair.alt')} />
    </button>
    <p class="muted small">
      {t('pair.hint', { left: Math.floor(left / 60) + ':' + String(left % 60).padStart(2, '0') })}
    </p>
    <div class="row">
      <button onclick={load}>{t('common.refresh')}</button>
      <button onclick={copy}>{t('pair.copy')}</button>
    </div>
    {#if showCode}<textarea readonly rows="3">{qr.code}</textarea>{/if}
    {#if zoom}
      <div class="zoom" use:portal role="presentation" onclick={() => (zoom = false)}>
        <img src="data:image/png;base64,{qr.png}" alt={t('pair.altZoom')} />
        <p>{t('pair.zoomClose')}</p>
      </div>
    {/if}
  {:else if err}
    <p class="muted">{err}</p>
  {:else}
    <p class="muted">{t('common.loading')}</p>
  {/if}
</div>

<svelte:window onkeydown={(e) => e.key === 'Escape' && (zoom = false)} />

<style>
  .pair {
    text-align: center;
  }
  .qr {
    padding: 0;
    border: 0;
    background: none;
    cursor: zoom-in;
  }
  .zoom {
    position: fixed;
    inset: 0;
    z-index: 30;
    background: rgb(0 0 0 / 70%);
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 12px;
    cursor: zoom-out;
  }
  .zoom img {
    width: min(80vh, 80vw);
    height: min(80vh, 80vw);
    padding: 16px;
  }
  .zoom p {
    color: #fff;
    margin: 0;
  }
  img {
    width: 200px;
    height: 200px;
    image-rendering: pixelated;
    background: #fff;
    border-radius: 6px;
    padding: 4px;
  }
  .row {
    display: flex;
    gap: 8px;
    justify-content: center;
  }
  textarea {
    width: 100%;
    margin-top: 8px;
    font-size: 11px;
  }
</style>
