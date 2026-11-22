<script>
  import { onMount } from 'svelte'
  import { api, errorText } from '../lib/api.js'

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
      notify('Lista omijania zapisana')
    } catch (e) {
      notify(errorText(e), true)
    } finally {
      busy = false
    }
  }
</script>

<div class="card">
  <h2>Omijanie serwera</h2>
  <p class="small">
    Domeny (razem z subdomenami) i adresy IP z tej listy wychodzą bezpośrednio przez telefon, z jego adresu IP
    operatora — np. bank, który blokuje adresy serwerów.
  </p>
  <div class="banner warn small">
    Ten ruch nie idzie przez tunel: telefon i jego operator widzą, z czym się łączysz. Używaj tylko HTTPS. Działa
    tylko dla urządzeń, które pytają o adresy DNS huba.
  </div>
  <form onsubmit={(e) => (e.preventDefault(), save())}>
    <label>
      Jedna pozycja w wierszu: domena (bank.pl) albo adres / sieć IPv4 (203.0.113.0/24)
      <textarea bind:value={text} rows="5" spellcheck="false" placeholder="bank.pl"></textarea>
    </label>
    {#if info && (info.domains?.length || info.cidrs?.length)}
      <p class="muted small">
        Adresy z DNS: {info.learned} · telefony, które mogą przenieść ruch: {info.relays}
        {#if info.relays === 0}<b> — brak, ruch idzie przez tunel</b>{/if}
      </p>
    {/if}
    <button class="primary" disabled={busy}>{busy ? 'Zapisuję…' : 'Zapisz'}</button>
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
