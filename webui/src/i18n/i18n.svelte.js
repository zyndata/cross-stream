// Active language of the server panel: the choice (auto | pl | en) lives in
// localStorage; Automatic follows navigator.language (under Home Assistant
// ingress: the HA user's language). t()/tn() read the $state, so every
// template that uses them re-renders on a switch.
import en from './en.json'
import pl from './pl.json'
import { resolveLang, translate, translateN, locale } from './core.js'

const dicts = { en, pl }
const KEY = 'xs-lang'

function saved() {
  try {
    const v = localStorage.getItem(KEY)
    return v === 'pl' || v === 'en' ? v : 'auto'
  } catch {
    return 'auto'
  }
}

const choice = saved()
export const lang = $state({ choice, active: resolveLang(choice, navigator.language) })
show()

// show puts the active language on the document (lang attribute, tab title).
function show() {
  document.documentElement.lang = lang.active
  document.title = t('app.title')
}

export function t(key, params) {
  return translate(dicts, lang.active, key, params)
}

export function tn(key, n, params) {
  return translateN(dicts, lang.active, key, n, params)
}

// loc is the BCP 47 tag of the active language (dates, numbers).
export function loc() {
  return locale(lang.active)
}

// num formats v with exactly `digits` decimals in the active language.
export function num(v, digits = 0) {
  return Number(v || 0).toLocaleString(loc(), { minimumFractionDigits: digits, maximumFractionDigits: digits })
}

// setLang switches at once and remembers the choice in this browser.
export function setLang(c) {
  lang.choice = c === 'pl' || c === 'en' ? c : 'auto'
  lang.active = resolveLang(lang.choice, navigator.language)
  show()
  try {
    if (lang.choice === 'auto') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, lang.choice)
  } catch {
    // storage blocked: the choice lasts until the page reloads
  }
}
