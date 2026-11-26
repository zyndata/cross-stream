// Formatting helpers; text and number format follow the active language.
import { t, tn, num } from '../i18n/i18n.svelte.js'

export function mbps(v) {
  if (!v) return '0'
  if (v < 0.1) return num(v * 1000) + ' kb/s'
  return num(v, v < 10 ? 2 : 1) + ' Mb/s'
}

export function megabytes(mb) {
  if (!mb) return '0 MB'
  if (mb >= 1000) return num(mb / 1000, 2) + ' GB'
  return num(mb, mb < 10 ? 1 : 0) + ' MB'
}

export function ms(v) {
  return num(v) + ' ms'
}

// modeLabel names a hub mode (BONDED, DIRECT, CONNECTING, OFFLINE).
export function modeLabel(m) {
  const k = 'mode.' + String(m).toLowerCase()
  const s = t(k)
  return s === k ? m : s
}

export const netLabel = { nr: '5G', lte: 'LTE', wcdma: '3G', gsm: '2G' }

const reasons = ['limit', 'battery', 'no_data', 'no_cellular', 'user']

// lastReason translates "leaving: limit" style daemon reasons.
export function lastReason(r) {
  if (!r) return ''
  if (r.startsWith('leaving: ')) {
    const k = r.slice(9)
    return reasons.includes(k) ? t('reason.' + k) : k
  }
  return t('reason.lost')
}

export function ago(time) {
  if (!time) return ''
  const s = Math.max(0, (Date.now() - new Date(time).getTime()) / 1000)
  if (s < 60) return t('ago.s', { n: Math.round(s) })
  if (s < 3600) return t('ago.min', { n: Math.round(s / 60) })
  return t('ago.h', { n: Math.round(s / 3600) })
}

// pathHint says what enrollment goes through: a connected phone or an
// enabled local link (LAN, Ethernet, modem — off by default).
export function pathHint(view) {
  const phones = (view.relays ?? []).filter((r) => r.connected).length
  const links = (view.uplinks ?? []).filter((u) => u.enabled).length
  return t('enroll.pathHint', { phones: tn('enroll.pathPhones', phones), links: tn('enroll.pathLinks', links) })
}
