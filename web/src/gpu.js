export const gpuKey = (gpu) => gpu.uuid || `index:${gpu.index}`
export const gpuLabel = (gpu) => `GPU${gpu.index} · ${gpu.name || 'NVIDIA GPU'}`

// nvidia-smi utilization.memory is memory bandwidth activity, not VRAM occupancy.
export function gpuMemoryPercent(gpu) {
  return Number.isFinite(gpu.memUsed) && Number.isFinite(gpu.memTotal) && gpu.memTotal > 0
    ? Math.min(100, Math.max(0, (gpu.memUsed / gpu.memTotal) * 100)) : null
}

export function appendGPUSnapshot(server, snapshot, limit) {
  server.gpuRing ||= {}
  const current = new Set()
  for (const gpu of snapshot.gpus || []) {
    const key = gpuKey(gpu)
    current.add(key)
    const series = server.gpuRing[key] ||= []
    series.push({ t: snapshot.ts, util: gpu.util, memPct: gpuMemoryPercent(gpu), temp: gpu.temp, power: gpu.powerDraw ?? null })
    if (series.length > limit) series.splice(0, series.length - limit)
  }
  for (const key of Object.keys(server.gpuRing)) if (!current.has(key)) delete server.gpuRing[key]
}
