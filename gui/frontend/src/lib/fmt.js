// Formatting helpers (Polish UI).

export function mbps(v) {
  if (!v) return '0'
  if (v < 0.1) return (v * 1000).toFixed(0) + ' kb/s'
  return (v < 10 ? v.toFixed(2) : v.toFixed(1)) + ' Mb/s'
}

export function megabytes(mb) {
  if (!mb) return '0 MB'
  if (mb >= 1000) return (mb / 1000).toFixed(2) + ' GB'
  return mb.toFixed(mb < 10 ? 1 : 0) + ' MB'
}

export function ms(v) {
  return v.toFixed(0) + ' ms'
}

export const modeLabel = {
  BONDED: 'Połączono (bonding)',
  DIRECT: 'Tryb bezpośredni',
  CONNECTING: 'Łączenie',
  OFFLINE: 'Offline',
}

export const netLabel = { nr: '5G', lte: 'LTE', wcdma: '3G', gsm: '2G' }

const reasonLabel = {
  limit: 'limit danych',
  battery: 'niski poziom baterii',
  no_data: 'brak danych komórkowych',
  no_cellular: 'brak sieci komórkowej',
  user: 'wyłączony w telefonie',
}

// lastReason translates "leaving: limit" style daemon reasons.
export function lastReason(r) {
  if (!r) return ''
  if (r.startsWith('leaving: ')) {
    const k = r.slice(9)
    return reasonLabel[k] ?? k
  }
  return 'utracono połączenie'
}

export function ago(t) {
  if (!t) return ''
  const s = Math.max(0, (Date.now() - new Date(t).getTime()) / 1000)
  if (s < 60) return Math.round(s) + ' s temu'
  if (s < 3600) return Math.round(s / 60) + ' min temu'
  return Math.round(s / 3600) + ' h temu'
}

// pathHint says what enrollment goes through: a connected phone or an
// enabled local link (LAN, Ethernet, modem — off by default).
export function pathHint(view) {
  const phones = (view.relays ?? []).filter((r) => r.connected).length
  const links = (view.uplinks ?? []).filter((u) => u.enabled).length
  const now = `Teraz: ${phones} ${phones === 1 ? 'telefon' : 'telefony'} połączone, ${links} ${links === 1 ? 'łącze lokalne włączone' : 'łącza lokalne włączone'}.`
  return (
    'Rejestracja idzie przez połączony telefon albo włączone łącze lokalne huba (zakładka Panel); ' +
    'gdy nie ma żadnego — przez połączenie internetowe tego komputera. ' + now
  )
}
