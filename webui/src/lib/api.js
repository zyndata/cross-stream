// JSON API of the server panel (internal/webui). Writes carry the
// session's CSRF token; a 401 anywhere means "log in again".
let csrf = ''

export function setCsrf(t) {
  csrf = t || ''
}

export class ApiError extends Error {
  constructor(status, message) {
    super(message)
    this.status = status
  }
}

async function req(method, path, body) {
  const opt = { method, headers: {}, credentials: 'same-origin', cache: 'no-store' }
  if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json'
    opt.body = JSON.stringify(body)
  }
  if (method !== 'GET') opt.headers['X-CSRF-Token'] = csrf
  const r = await fetch('api/' + path, opt) // relative: works under HA ingress' path prefix
  let data = null
  try {
    data = await r.json()
  } catch {
    data = null
  }
  if (!r.ok) throw new ApiError(r.status, data?.error || r.statusText)
  return data
}

export const api = {
  session: () => req('GET', 'session'),
  login: (user, password, code) => req('POST', 'login', { user, password, code }),
  logout: () => req('POST', 'logout', {}),
  hubs: () => req('GET', 'hubs'),
  sessions: () => req('GET', 'sessions'),
  info: () => req('GET', 'info'),
  audit: (n = 200) => req('GET', 'audit?n=' + n),
  logs: () => req('GET', 'logs'),
  enroll: (name) => req('POST', 'hubs/enroll', { name }),
  revoke: (name) => req('POST', 'hubs/revoke', { name }),
  rename: (name, new_name) => req('POST', 'hubs/rename', { name, new_name }),
  forward: (name, n) => req('POST', 'hubs/forward', { name, n }),
  tunnelPort: (port) => req('POST', 'server/tunnel-port', { port }),
  restart: () => req('POST', 'server/restart', {}),
}

// stream opens the live SSE feed: history once, then a tick per second.
export function stream({ history, tick, logout, error }) {
  const es = new EventSource('api/stream')
  es.addEventListener('history', (e) => history(JSON.parse(e.data)))
  es.addEventListener('tick', (e) => tick(JSON.parse(e.data)))
  es.addEventListener('logout', () => {
    es.close()
    logout()
  })
  es.onerror = () => error?.()
  return es
}
