<script>
  import { api, setCsrf } from '../lib/api.js'
  import LangSwitch from './LangSwitch.svelte'
  import { t } from '../i18n/i18n.svelte.js'

  let { onlogin } = $props()
  let user = $state('')
  let password = $state('')
  let code = $state('')
  let needCode = $state(false)
  let error = $state('')
  let busy = $state(false)

  async function submit(e) {
    e.preventDefault()
    busy = true
    error = ''
    try {
      const s = await api.login(user.trim(), password, code.trim())
      setCsrf(s.csrf)
      password = ''
      code = ''
      onlogin(s)
    } catch (err) {
      if (err.message === 'totp_required') {
        needCode = true
        error = t('login.needCode')
      } else if (err.status === 429) {
        error = t('login.tooMany')
      } else {
        error = t('login.bad')
      }
    } finally {
      busy = false
    }
  }
</script>

<div class="wrap">
  <form class="card" onsubmit={submit}>
    <div class="top"><h1>CrossStream</h1><LangSwitch /></div>
    <p class="muted">{t('login.subtitle')}</p>
    <label>{t('login.user')} <input bind:value={user} autocomplete="username" required /></label>
    <label>{t('login.password')} <input type="password" bind:value={password} autocomplete="current-password" required /></label>
    {#if needCode}
      <!-- svelte-ignore a11y_autofocus -->
      <label>{t('login.code')} <input bind:value={code} inputmode="numeric" autocomplete="one-time-code" maxlength="6" autofocus /></label>
    {/if}
    {#if error}<div class="banner bad">{error}</div>{/if}
    <button class="primary" disabled={busy}>{t('login.submit')}</button>
    <p class="muted small">{t('login.hint')} <code>xs-ctl admin add &lt;{t('login.userArg')}&gt;</code></p>
  </form>
</div>

<style>
  .wrap {
    min-height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 16px;
  }
  form {
    width: min(360px, 100%);
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  h1 {
    margin: 0;
    font-size: 20px;
  }
  .top {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }
  p {
    margin: 0;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .banner {
    margin: 0;
  }
</style>
