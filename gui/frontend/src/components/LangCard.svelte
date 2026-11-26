<script>
  import { api, errorText } from '../lib/api.js'
  import { t, lang, applyLang } from '../i18n/i18n.svelte.js'

  let { notify } = $props()

  // Language names stay in their own language; the title shows both, so a
  // user who cannot read the current one still finds it.
  const choices = [
    ['auto', 'lang.auto'],
    ['pl', 'lang.pl'],
    ['en', 'lang.en'],
  ]

  async function save(v) {
    try {
      applyLang(await api.setLanguage(v))
    } catch (e) {
      notify(errorText(e), true)
    }
  }
</script>

<div class="card">
  <h2>{t('lang.title')}</h2>
  {#each choices as [v, key]}
    <label class="check">
      <input type="radio" name="lang" checked={lang.choice === v} onchange={() => save(v)} />
      {t(key)}
    </label>
  {/each}
</div>

<style>
  .check {
    display: flex;
    gap: 8px;
    align-items: center;
    margin: 6px 0;
  }
</style>
