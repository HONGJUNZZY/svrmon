import test from 'node:test'
import assert from 'node:assert/strict'
import { appendGPUSnapshot, gpuKey, gpuMemoryPercent } from './gpu.js'

test('VRAM occupancy is independent of memory bandwidth utilization', () => {
  assert.equal(gpuMemoryPercent({ memUsed: 100, memTotal: 1000, memUtil: 99 }), 10)
  assert.equal(gpuMemoryPercent({ memUsed: 0, memTotal: 1000, memUtil: 99 }), 0)
  assert.equal(gpuMemoryPercent({ memUsed: 100, memTotal: 0 }), null)
})

test('all GPUs retain independent series across non-contiguous indices and ordering', () => {
  const server = {}
  const gpus = Array.from({ length: 12 }, (_, i) => ({ uuid: `gpu-${i}`, index: i * 2, util: i, memUsed: i * 10, memTotal: 1000, powerDraw: i }))
  appendGPUSnapshot(server, { ts: 1, gpus }, 2)
  appendGPUSnapshot(server, { ts: 2, gpus: [...gpus].reverse().map((g) => ({ ...g, util: g.util + 10 })) }, 2)
  assert.equal(Object.keys(server.gpuRing).length, 12)
  for (const gpu of gpus) assert.deepEqual(server.gpuRing[gpuKey(gpu)].map((p) => p.util), [gpu.util, gpu.util + 10])
  appendGPUSnapshot(server, { ts: 3, gpus }, 2)
  for (const points of Object.values(server.gpuRing)) assert.deepEqual(points.map((p) => p.t), [2, 3])
})

test('replaced GPU does not inherit another card history and unsupported power is null', () => {
  const server = {}
  appendGPUSnapshot(server, { ts: 1, gpus: [{ uuid: 'old', index: 0 }] }, 240)
  appendGPUSnapshot(server, { ts: 2, gpus: [{ uuid: 'new', index: 0 }] }, 240)
  assert.deepEqual(Object.keys(server.gpuRing), ['new'])
  assert.equal(server.gpuRing.new[0].power, null)
})
