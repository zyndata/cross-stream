// Fails the build when dist/ needs anything the panel's CSP forbids
// (default-src 'self', no 'unsafe-inline', no 'unsafe-eval'): inline
// scripts or styles, style="…" attributes, inline event handlers, eval,
// injected <style> elements, data: or remote URLs.
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, extname } from 'node:path'
import { fileURLToPath } from 'node:url'

// Usage: node scripts/csp-check.mjs [dir] (default: dist/).
const dist = process.argv[2] || fileURLToPath(new URL('../dist/', import.meta.url))
const files = []
const walk = (d) => {
  for (const f of readdirSync(d)) {
    const p = join(d, f)
    if (statSync(p).isDirectory()) walk(p)
    else files.push(p)
  }
}
walk(dist)

const rules = {
  '.html': [
    [/<script(?![^>]*\bsrc=)[^>]*>/i, 'inline <script>'],
    [/<style[\s>]/i, '<style> element'],
    [/\sstyle\s*=/i, 'style="…" attribute'],
    [/\son[a-z]+\s*=/i, 'inline event handler'],
    [/(src|href)\s*=\s*["']?(https?:|data:|\/\/)/i, 'remote or data: URL'],
    [/(src|href)\s*=\s*["']?\//i, 'absolute path (HA ingress needs relative URLs)'],
  ],
  '.js': [
    [/\sstyle\s*=\s*\\?["']/, 'style="…" in a template'],
    [/setAttribute\(\s*["']style["']/, 'setAttribute("style")'],
    [/createElement\(\s*["']style["']/, 'injected <style>'],
    [/\beval\s*\(/, 'eval()'],
    [/\bnew Function\s*\(/, 'new Function()'],
    [/\son(click|load|error|input|submit)\s*=\s*\\?["']/, 'inline event handler in a template'],
  ],
  '.css': [
    [/url\(\s*["']?(data:|https?:|\/\/)/i, 'data: or remote url() in CSS'],
    [/@import/i, '@import'],
  ],
}

let bad = 0
for (const f of files) {
  const r = rules[extname(f)]
  if (!r) continue
  const text = readFileSync(f, 'utf8')
  for (const [re, what] of r) {
    const m = text.match(re)
    if (m) {
      bad++
      const at = Math.max(0, m.index - 40)
      console.error(`CSP violation in ${f}: ${what}: …${text.slice(at, m.index + 60)}…`)
    }
  }
}
if (bad) {
  console.error(`csp-check: ${bad} violation(s)`)
  process.exit(1)
}
console.log(`csp-check: ${files.length} files, 0 violations`)
