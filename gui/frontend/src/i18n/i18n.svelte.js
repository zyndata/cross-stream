// Active language of the hub GUI. Go keeps the choice (gui-lang.json) and
// resolves Automatic from the system locale; t()/tn() read the $state, so
// every template that uses them re-renders on a switch.
import en from './en.json'
import pl from './pl.json'
import { resolveLang, translate, translateN, locale } from './core.js'

const dicts = { en, pl }

export const lang = $state({ choice: 'auto', active: resolveLang('auto', navigator.language) })

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

// applyLang takes {choice, lang} from Go (Language / SetLanguage).
export function applyLang(v) {
  if (!v) return
  lang.choice = v.choice || 'auto'
  lang.active = v.lang === 'pl' || v.lang === 'en' ? v.lang : resolveLang(lang.choice, navigator.language)
  document.documentElement.lang = lang.active
}
