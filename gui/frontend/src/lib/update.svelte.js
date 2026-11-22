// Update state shared by the banner (App.svelte), the Settings card and the
// install confirmation: the backend sends it as "update" events.
import { api, onUpdate } from './api.js'

export const upd = $state({ v: null, confirm: false })

// startUpdates loads the state once and follows the events; returns the unsubscribe.
export function startUpdates() {
  api
    .updateInfo()
    .then((v) => (upd.v = v))
    .catch(() => {})
  return onUpdate((v) => (upd.v = v))
}

// sizeMB is an asset size in MB for the confirmation ("≈ 24 MB").
export function sizeMB(bytes) {
  return Math.max(1, Math.round((bytes ?? 0) / 1e6))
}
