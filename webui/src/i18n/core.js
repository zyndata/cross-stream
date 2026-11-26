// Language core shared by the hub GUI and the server panel (the same file in
// gui/frontend/src/i18n and webui/src/i18n; a Go test keeps them equal).
// No framework code here, so a Go test can run it with node.

export const LANGS = ['pl', 'en']

// resolveLang maps a choice (auto | pl | en) and a system language tag to pl or en.
export function resolveLang(choice, system) {
  if (choice === 'pl' || choice === 'en') return choice
  return /^pl\b/i.test(String(system || '')) ? 'pl' : 'en'
}

// pluralIndex picks the plural form: Polish 1 / 2–4 (not 12–14) / other,
// English 1 / other.
export function pluralIndex(lang, n) {
  n = Math.abs(Math.trunc(Number(n) || 0))
  if (lang !== 'pl') return n === 1 ? 0 : 1
  if (n === 1) return 0
  const d = n % 10
  const h = n % 100
  return d >= 2 && d <= 4 && (h < 12 || h > 14) ? 1 : 2
}

// fill replaces {name} placeholders with params[name]; unknown ones stay.
export function fill(s, params) {
  if (!params) return s
  return s.replace(/\{(\w+)\}/g, (m, k) => (k in params ? String(params[k]) : m))
}

// pick returns the plural form of a "one|few|many" (Polish) or "one|other"
// (English) string for n.
export function pick(s, lang, n) {
  const forms = s.split('|')
  return forms[Math.min(pluralIndex(lang, n), forms.length - 1)]
}

// locale is the BCP 47 tag for dates and numbers.
export function locale(lang) {
  return lang === 'pl' ? 'pl-PL' : 'en-GB'
}

// translate looks key up in dict, then in fallback, else returns the key.
export function translate(dicts, lang, key, params) {
  const s = dicts[lang]?.[key] ?? dicts.en?.[key] ?? key
  return fill(s, params)
}

// translateN is translate for a plural string; params.n defaults to n.
export function translateN(dicts, lang, key, n, params) {
  const s = dicts[lang]?.[key] ?? dicts.en?.[key] ?? key
  return fill(pick(s, lang, n), { n, ...params })
}
