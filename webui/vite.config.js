import { defineConfig } from 'vite'
import { svelte } from '@sveltejs/vite-plugin-svelte'

// The panel runs under CSP default-src 'self' (internal/webui): no inline
// assets (data: URIs), no injected <style> elements, one external CSS file.
export default defineConfig({
  // Relative asset URLs: the panel also runs under Home Assistant ingress
  // (/api/hassio_ingress/<token>/), whose prefix the server never sees.
  base: './',
  plugins: [svelte({ compilerOptions: { css: 'external' } })],
  build: {
    target: 'es2020',
    assetsInlineLimit: 0,
    cssCodeSplit: false,
    modulePreload: { polyfill: false },
    chunkSizeWarningLimit: 600,
  },
  server: {
    // npm run dev: API calls go to a running xs-server panel.
    proxy: { '/api': { target: 'https://localhost:8443', secure: false } },
  },
})
