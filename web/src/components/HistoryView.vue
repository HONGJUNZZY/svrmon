<template>
  <div>
    <div class="range-bar">
      <button
        v-for="r in ranges"
        :key="r.sec"
        class="range-btn"
        :class="{ active: rangeSec === r.sec }"
        @click="rangeSec = r.sec"
      >
        {{ r.label }}
      </button>
      <span v-if="loading" class="muted" style="align-self: center; margin-left: 8px">加载中…</span>
      <span v-else-if="err" class="status-bad" style="align-self: center; margin-left: 8px">{{ err }}</span>
      <span v-else-if="!pts.length" class="muted" style="align-self: center; margin-left: 8px">
        该时段无历史数据（hub 每 10 秒落一次盘）
      </span>
    </div>

    <div v-if="pts.length" class="charts-grid">
      <div class="card chart-card"><p class="panel-title">磁盘读写（合计）</p><Chart :option="diskOpt" /></div>
      <div v-if="diskNames.length" class="card chart-card"><p class="panel-title">各磁盘读写（实线读 / 虚线写）</p><Chart :option="diskDetailOpt" /></div>
      <div v-if="diskNames.length" class="card chart-card"><p class="panel-title">各磁盘繁忙度</p><Chart :option="diskUtilOpt" /></div>
      <div class="card chart-card"><p class="panel-title">CPU / iowait</p><Chart :option="cpuOpt" /></div>
      <div class="card chart-card"><p class="panel-title">内存</p><Chart :option="memOpt" /></div>
      <div class="card chart-card"><p class="panel-title">网络吞吐</p><Chart :option="netOpt" /></div>
      <div v-if="hasGpu" class="card chart-card"><p class="panel-title">GPU 利用率</p><Chart :option="gpuOpt" /></div>
      <div v-if="hasTemp" class="card chart-card"><p class="panel-title">温度（CPU / GPU）</p><Chart :option="tempOpt" /></div>
      <div v-if="hasPower" class="card chart-card"><p class="panel-title">功耗（GPU / 整机 IPMI）</p><Chart :option="powerOpt" /></div>
      <div v-if="hasFan" class="card chart-card"><p class="panel-title">风扇转速（最高）</p><Chart :option="fanOpt" /></div>
      <div v-if="hasLogs" class="card chart-card"><p class="panel-title">严重日志（条数 / 桶）</p><Chart :option="logOpt" /></div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { getHistory } from '../api.js'
import { fmtBytes, fmtBytesAbs } from '../api.js'
import { lineOption } from '../charts.js'
import Chart from './Chart.vue'

const props = defineProps({ server: { type: String, required: true } })

const ranges = [
  { label: '1小时', sec: 3600 },
  { label: '6小时', sec: 21600 },
  { label: '24小时', sec: 86400 },
  { label: '3天', sec: 259200 },
  { label: '7天', sec: 604800 },
]
const rangeSec = ref(86400)
const pts = ref([])
const diskSeries = ref({}) // name -> [{t, r, w, util}]
const loading = ref(false)
const err = ref('')

async function load() {
  loading.value = true
  err.value = ''
  const to = Math.floor(Date.now() / 1000)
  const from = to - rangeSec.value
  try {
    const r = await getHistory(props.server, from, to, 900)
    pts.value = r.points || []
    diskSeries.value = r.disks || {}
  } catch (e) {
    err.value = e.message
    pts.value = []
    diskSeries.value = {}
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch(rangeSec, load)
watch(() => props.server, load)

const X = (key) => pts.value.map((p) => [p.t * 1000, p[key]])
const hasGpu = computed(() => pts.value.some((p) => p.gpuUtil != null))
const hasTemp = computed(() => pts.value.some((p) => p.cpuTemp != null || p.gpuTemp != null))
const hasPower = computed(() => pts.value.some((p) => p.gpuPower != null || p.ipmiPower != null))
const hasFan = computed(() => pts.value.some((p) => p.fanMax != null))
const hasLogs = computed(() => pts.value.some((p) => p.logCount != null))

const cpuOpt = computed(() =>
  lineOption({
    yFmt: (v) => v.toFixed(0) + '%',
    max: 100,
    series: [
      { name: 'CPU', color: '#4cc2ff', area: true, data: X('cpu') },
      { name: 'iowait', color: '#d29922', data: X('iowait') },
    ],
  }),
)
const memOpt = computed(() =>
  lineOption({
    yFmt: (v) => fmtBytesAbs(v),
    series: [
      { name: '已用', color: '#a371f7', area: true, data: X('memUsed') },
      { name: '总量', color: '#7b8a9c', data: X('memTotal') },
    ],
  }),
)
const diskOpt = computed(() =>
  lineOption({
    yFmt: (v) => fmtBytes(v),
    series: [
      { name: '读', color: '#3fb950', data: X('diskR') },
      { name: '写', color: '#f0883e', data: X('diskW') },
      { name: '读峰值', color: '#3fb950', dashed: true, data: X('diskRMax') },
      { name: '写峰值', color: '#f0883e', dashed: true, data: X('diskWMax') },
    ],
  }),
)
const diskPalette = ['#4cc2ff', '#3fb950', '#f0883e', '#a371f7', '#e599f7', '#d29922', '#58a6ff', '#ff7b72']
const diskNames = computed(() => Object.keys(diskSeries.value).sort())
const diskDetailOpt = computed(() => {
  const series = []
  diskNames.value.forEach((name, i) => {
    const color = diskPalette[i % diskPalette.length]
    const p = diskSeries.value[name] || []
    series.push({ name: name + ' 读', color, data: p.map((x) => [x.t * 1000, x.r]) })
    series.push({ name: name + ' 写', color, dashed: true, data: p.map((x) => [x.t * 1000, x.w]) })
    series.push({ name: name + ' 读峰', color, lineType: 'dotted', dim: true, data: p.map((x) => [x.t * 1000, x.rMax]) })
    series.push({ name: name + ' 写峰', color, lineType: 'dotted', dim: true, data: p.map((x) => [x.t * 1000, x.wMax]) })
  })
  return lineOption({ yFmt: (v) => fmtBytes(v), series })
})
const diskUtilOpt = computed(() => {
  const series = []
  diskNames.value.forEach((name, i) => {
    const color = diskPalette[i % diskPalette.length]
    const p = diskSeries.value[name] || []
    series.push({ name, color, data: p.map((x) => [x.t * 1000, x.util]) })
  })
  return lineOption({ yFmt: (v) => v.toFixed(0) + '%', max: 100, series })
})
const netOpt = computed(() =>
  lineOption({
    yFmt: (v) => fmtBytes(v),
    series: [
      { name: '下行', color: '#4cc2ff', area: true, data: X('netRx') },
      { name: '上行', color: '#e599f7', data: X('netTx') },
    ],
  }),
)
const gpuOpt = computed(() =>
  lineOption({
    yFmt: (v) => v.toFixed(0) + '%',
    max: 100,
    series: [{ name: 'GPU 利用率', color: '#3fb950', area: true, data: X('gpuUtil') }],
  }),
)
const tempOpt = computed(() =>
  lineOption({
    yFmt: (v) => v.toFixed(0) + '°C',
    series: [
      { name: 'CPU 温度', color: '#f0883e', data: X('cpuTemp') },
      { name: 'GPU 温度', color: '#3fb950', data: X('gpuTemp') },
    ],
  }),
)
const powerOpt = computed(() =>
  lineOption({
    yFmt: (v) => v.toFixed(0) + ' W',
    series: [
      { name: 'GPU 功耗', color: '#3fb950', area: true, data: X('gpuPower') },
      { name: '整机功耗 (IPMI)', color: '#4cc2ff', data: X('ipmiPower') },
    ],
  }),
)
const fanOpt = computed(() =>
  lineOption({
    yFmt: (v) => v.toFixed(0) + ' RPM',
    series: [{ name: '风扇最高转速', color: '#a371f7', data: X('fanMax') }],
  }),
)
const logOpt = computed(() =>
  lineOption({
    yFmt: (v) => v.toFixed(0) + ' 条',
    min: 0,
    series: [{ name: '严重日志条数', color: '#ff7b72', area: true, data: X('logCount') }],
  }),
)
</script>
