import { reactive } from 'vue'

// Global live state fed by the hub WebSocket.
export const state = reactive({
  connected: false,
  hubVer: '',
  servers: {}, // name -> { info, online, lastSeen, latest, ring: [] }
})

const RING_MAX = 240 // ~8 minutes at a 2s snapshot interval

export function token() {
  return localStorage.getItem('svrmon_token') || ''
}
export function setToken(t) {
  localStorage.setItem('svrmon_token', t)
  connect(true)
}

function ringPush(st, snap) {
  const cpu = snap.cpu ? snap.cpu.usage : null
  const iowait = snap.cpu ? snap.cpu.iowait : null
  const memPct = snap.mem && snap.mem.total ? (snap.mem.used / snap.mem.total) * 100 : null
  let gpuUtil = null
  let gpuTemp = null
  let gpuPower = null
  if (snap.gpus && snap.gpus.length) {
    let su = 0
    let tmax = 0
    let pw = 0
    for (const g of snap.gpus) {
      su += g.util
      if (g.temp > tmax) tmax = g.temp
      if (g.powerDraw != null) pw += g.powerDraw
    }
    gpuUtil = su / snap.gpus.length
    gpuTemp = tmax
    gpuPower = pw
  }
  let diskR = 0
  let diskW = 0
  let netRx = 0
  let netTx = 0
  st.diskRing = st.diskRing || {}
  for (const d of snap.disks || []) {
    diskR += d.readBps
    diskW += d.writeBps
    if (!st.diskRing[d.name]) st.diskRing[d.name] = []
    st.diskRing[d.name].push({ t: snap.ts, r: d.readBps, w: d.writeBps })
    if (st.diskRing[d.name].length > RING_MAX) st.diskRing[d.name].shift()
  }
  for (const n of snap.nets || []) {
    netRx += n.rxBps
    netTx += n.txBps
  }
  st.ring.push({ t: snap.ts, cpu, iowait, memPct, gpuUtil, gpuTemp, gpuPower, diskR, diskW, netRx, netTx })
  if (st.ring.length > RING_MAX) st.ring.shift()
}

function ensure(name, info) {
  if (!state.servers[name]) {
    state.servers[name] = { info: info || { hostname: name }, online: false, lastSeen: 0, latest: null, ring: [], diskRing: {}, logs: [] }
  } else if (info) {
    state.servers[name].info = info
  }
  return state.servers[name]
}

let ws = null
let retryMs = 1000

export function connect(force = false) {
  if (ws && !force) return
  if (ws) {
    try {
      ws.close()
    } catch {}
    ws = null
  }
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const t = token()
  const url = `${proto}://${location.host}/ws${t ? '?token=' + encodeURIComponent(t) : ''}`
  ws = new WebSocket(url)
  ws.onopen = () => {
    state.connected = true
    retryMs = 1000
  }
  ws.onclose = () => {
    state.connected = false
    setTimeout(() => connect(), retryMs)
    retryMs = Math.min(retryMs * 2, 15000)
  }
  ws.onmessage = (ev) => {
    let m
    try {
      m = JSON.parse(ev.data)
    } catch {
      return
    }
    switch (m.type) {
      case 'init':
        state.hubVer = m.hubVer || ''
        for (const [name, st] of Object.entries(m.servers || {})) {
          const s = ensure(name, st.info)
          s.online = st.online
          s.lastSeen = st.lastSeen
          s.latest = st.latest
          s.config = st.config || null
          s.ring = []
        }
        break
      case 'join':
        ensure(m.server, m.info).online = true
        break
      case 'snapshot': {
        const s = ensure(m.server)
        s.online = true
        s.lastSeen = Math.floor(Date.now() / 1000)
        s.latest = m.data
        ringPush(s, m.data)
        if (m.data?.logs?.length) {
          s.logs = s.logs || []
          for (const e of m.data.logs) s.logs.push(e)
          if (s.logs.length > 500) s.logs.splice(0, s.logs.length - 500)
        }
        break
      }
      case 'offline': {
        const s = state.servers[m.server]
        if (s) s.online = false
        break
      }
      case 'rename': {
        const s = state.servers[m.from]
        if (s) {
          state.servers[m.to] = s
          delete state.servers[m.from]
        }
        break
      }
      case 'config': {
        const s = state.servers[m.server]
        if (s) s.config = m.data
        break
      }
    }
  }
}
