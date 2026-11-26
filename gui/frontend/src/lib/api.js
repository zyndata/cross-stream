// Thin wrapper over the Wails bindings (window.go / window.runtime), so the
// generated wailsjs/ folder is not needed. Outside Wails (vite dev in a
// browser) every call rejects and the view stays in the "daemon" state.

import { t } from '../i18n/i18n.svelte.js'

const app = () => window.go?.main?.App

function call(name, ...args) {
  const a = app()
  if (!a) return Promise.reject(new Error('daemon: ' + t('err.noBackend')))
  return a[name](...args)
}

export const api = {
  current: () => call('Current'),
  platform: () => call('Platform'),
  pairQR: () => call('PairQR'),
  setRelay: (id, params) => call('SetRelay', id, params),
  forget: (id) => call('Forget', id),
  setOverflow: (mbps) => call('SetOverflow', mbps),
  hotspot: () => call('Hotspot'),
  setHotspot: (s) => call('SetHotspot', s),
  hotspotPower: (on) => call('HotspotPower', on),
  clientWifiChoice: (choice) => call('ClientWifiChoice', choice),
  enroll: (code) => call('Enroll', code),
  setBond: (mode, redundancy) => call('SetBond', mode, redundancy),
  pepSettings: () => call('PEPSettings'),
  setPEP: (on, exclude) => call('SetPEP', on, exclude),
  setOwnTraffic: (on) => call('SetOwnTraffic', on),
  speedTest: (p) => call('SpeedTest', p),
  speedTests: () => call('SpeedTests'),
  bypass: () => call('Bypass'),
  setBypass: (domains, cidrs) => call('SetBypass', domains, cidrs),
  forwards: () => call('Forwards'),
  setForwards: (list) => call('SetForwards', list),
  usage: (from) => call('Usage', from),
  notifyPrefs: () => call('NotifyPrefs'),
  setNotifyPrefs: (p) => call('SetNotifyPrefs', p),
  exportLogs: () => call('ExportLogs'),
  setVerboseLogs: (on) => call('SetVerboseLogs', on),
}

export function onView(fn) {
  return window.runtime?.EventsOn?.('view', fn) ?? (() => {})
}

// errorText turns a backend error ("state: message") into text in the active language.
export function errorText(err) {
  const s = String(err?.message ?? err ?? '')
  const i = s.indexOf(': ')
  const state = i > 0 ? s.slice(0, i) : ''
  const msg = i > 0 ? s.slice(i + 2) : s
  switch (state) {
    case 'daemon':
      return t('err.daemon')
    case 'group':
      return t('err.group')
    case 'error':
      return msg
  }
  return s
}

// Phase 12: local links and xs-hubd service control.
api.setUplink = (params) => call('SetUplink', params)
api.forgetUplink = (id) => call('ForgetUplink', id)
api.captiveLogin = (id) => call('CaptiveLogin', id)
api.closeChoice = (choice, remember) => call('CloseChoice', choice, remember)
api.closePref = () => call('ClosePref')
api.setClosePref = (v) => call('SetClosePref', v)
api.serviceControl = () => call('ServiceControl')
api.startHub = () => call('StartHub')

export function onCloseAsk(fn) {
  return window.runtime?.EventsOn?.('close-ask', fn) ?? (() => {})
}

// Updates (gui/updates.go).
api.updateInfo = () => call('UpdateInfo')
api.checkUpdate = () => call('CheckUpdate')
api.setUpdateAuto = (on) => call('SetUpdateAuto', on)
api.dismissUpdate = () => call('DismissUpdate')
api.installUpdate = () => call('InstallUpdate')
api.openURL = (url) => call('OpenURL', url)

export function onUpdate(fn) {
  return window.runtime?.EventsOn?.('update', fn) ?? (() => {})
}

// enrollErrorText turns an enrollment failure into a hint (phase 19); the
// backend substrings are English in both languages.
export function enrollErrorText(err) {
  const s = errorText(err)
  if (s.includes('no answer from the server through')) return t('enroll.err.noAnswer')
  if (s.includes('no handshake response')) return t('enroll.err.noHandshake')
  if (s.includes('an enrollment is already running')) return t('enroll.err.running')
  return s
}

// Language (gui/lang.go): {choice, lang}.
api.language = () => call('Language')
api.setLanguage = (choice) => call('SetLanguage', choice)
