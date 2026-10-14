import { token } from './store.js'

async function req(path, opts = {}) {
  const headers = { ...(opts.headers || {}) }
  const t = token()
  if (t) headers['Authorization'] = 'Bearer ' + t
  if (opts.body) headers['Content-Type'] = 'application/json'
  const r = await fetch(path, { ...opts, headers })
  if (!r.ok) {
    let msg = r.statusText
    try {
      const j = await r.json()
      msg = j.error || msg
    } catch {}
    throw new Error(msg)
  }
  return r.json()
}

export const getServers = () => req('/api/servers')
export const getHistory = (server, from, to, points = 900) =>
  req(`/api/history?server=${encodeURIComponent(server)}&from=${from}&to=${to}&points=${points}`)
export const postPower = (server, action) =>
  req('/api/power', { method: 'POST', body: JSON.stringify({ server, action }) })
export const postRename = (server, name) =>
  req('/api/rename', { method: 'POST', body: JSON.stringify({ server, name }) })
export const getLogs = (server, from, to, limit = 500) =>
  req(`/api/logs?server=${encodeURIComponent(server)}&from=${from}&to=${to}&limit=${limit}`)

function fmt(v, units, digits) {
  if (v == null || isNaN(v)) return '—'
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return v.toFixed(v >= 100 ? 0 : digits) + ' ' + units[i]
}

// rates (B/s)
export const fmtBytes = (v) => fmt(v, ['B/s', 'KB/s', 'MB/s', 'GB/s', 'TB/s'], 1)
// absolute sizes
export const fmtBytesAbs = (v) => fmt(v, ['B', 'KB', 'MB', 'GB', 'TB'], 1)

export function fmtPct(v, digits = 1) {
  return v == null ? '—' : v.toFixed(digits) + '%'
}

export function fmtTemp(v) {
  return v ? v.toFixed(0) + '°C' : '—'
}

export function fmtDur(sec) {
  if (sec == null) return '—'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d) return `${d}天${h}时`
  if (h) return `${h}时${m}分`
  return `${m}分`
}
