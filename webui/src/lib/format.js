// Display helpers; text and number format follow the active language.
import { t, num, loc } from '../i18n/i18n.svelte.js'

export function mbps(bytesPerSec) {
  const v = ((bytesPerSec || 0) * 8) / 1e6
  return num(v, v < 10 ? 2 : 1) + ' Mb/s'
}

export function bytes(n) {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let i = 0
  n = n || 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return num(n, i === 0 ? 0 : 1) + ' ' + units[i]
}

// ms formats a Go time.Duration (nanoseconds in JSON).
export function ms(ns) {
  return num(Math.round((ns || 0) / 1e6)) + ' ms'
}

export function pct(f) {
  return num((f || 0) * 100, 1) + ' %'
}

export function when(time) {
  if (!time || time.startsWith('0001-')) return '—'
  return new Date(time).toLocaleString(loc())
}

export function ago(time, now = Date.now()) {
  if (!time || time.startsWith('0001-')) return t('ago.never')
  const s = Math.max(0, Math.round((now - new Date(time).getTime()) / 1000))
  if (s < 60) return t('ago.s', { n: s })
  if (s < 3600) return t('ago.min', { n: Math.round(s / 60) })
  return t('ago.h', { n: Math.round(s / 3600) })
}

const states = ['alive', 'suspect', 'sleeping', 'dead']
export function pathState(s) {
  return states.includes(s) ? t('path.' + s) : s || '—'
}

const prios = ['normal', 'secondary', 'backup']
export function priority(p) {
  return prios.includes(p) ? t('prio.' + p) : p || '—'
}
