// Display helpers (Polish UI).
export function mbps(bytesPerSec) {
  const v = ((bytesPerSec || 0) * 8) / 1e6
  return v < 10 ? v.toFixed(2) + ' Mb/s' : v.toFixed(1) + ' Mb/s'
}

export function bytes(n) {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let i = 0
  n = n || 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return (i === 0 ? n.toFixed(0) : n.toFixed(1)) + ' ' + units[i]
}

// ms formats a Go time.Duration (nanoseconds in JSON).
export function ms(ns) {
  return Math.round((ns || 0) / 1e6) + ' ms'
}

export function pct(f) {
  return ((f || 0) * 100).toFixed(1) + ' %'
}

export function when(t) {
  if (!t || t.startsWith('0001-')) return '—'
  return new Date(t).toLocaleString('pl-PL')
}

export function ago(t, now = Date.now()) {
  if (!t || t.startsWith('0001-')) return 'nigdy'
  const s = Math.max(0, Math.round((now - new Date(t).getTime()) / 1000))
  if (s < 60) return s + ' s temu'
  if (s < 3600) return Math.round(s / 60) + ' min temu'
  return Math.round(s / 3600) + ' h temu'
}

const states = { alive: 'działa', suspect: 'niepewna', sleeping: 'uśpiona', dead: 'nie działa' }
export function pathState(s) {
  return states[s] || s || '—'
}

const prios = { normal: 'normalny', secondary: 'dodatkowy', backup: 'zapasowy' }
export function priority(p) {
  return prios[p] || p || '—'
}
