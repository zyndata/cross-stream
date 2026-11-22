<script>
  import { api, setCsrf } from '../lib/api.js'

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
        error = 'Podaj kod z aplikacji uwierzytelniającej.'
      } else if (err.status === 429) {
        error = 'Za dużo prób logowania. Spróbuj ponownie za kilka minut.'
      } else {
        error = 'Nieprawidłowy użytkownik, hasło lub kod.'
      }
    } finally {
      busy = false
    }
  }
</script>

<div class="wrap">
  <form class="card" onsubmit={submit}>
    <h1>CrossStream</h1>
    <p class="muted">Panel serwera</p>
    <label>Użytkownik <input bind:value={user} autocomplete="username" required /></label>
    <label>Hasło <input type="password" bind:value={password} autocomplete="current-password" required /></label>
    {#if needCode}
      <!-- svelte-ignore a11y_autofocus -->
      <label>Kod 2FA <input bind:value={code} inputmode="numeric" autocomplete="one-time-code" maxlength="6" autofocus /></label>
    {/if}
    {#if error}<div class="banner bad">{error}</div>{/if}
    <button class="primary" disabled={busy}>Zaloguj</button>
    <p class="muted small">Konto tworzy się na serwerze: <code>xs-ctl admin add &lt;użytkownik&gt;</code></p>
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
