<script>
  // Language switch (header and login screen); remembered in this browser.
  import { t, lang, setLang } from '../i18n/i18n.svelte.js'
</script>

<select class="lang" aria-label={t('lang.label')} title={t('lang.label')} value={lang.choice}
  onchange={(e) => setLang(e.currentTarget.value)}>
  <option value="auto">{t('lang.auto')}</option>
  <option value="pl">{t('lang.pl')}</option>
  <option value="en">{t('lang.en')}</option>
</select>
