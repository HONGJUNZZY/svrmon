<template>
  <div>
    <div v-if="!server" class="empty">等待 {{ name }} 的数据…</div>
    <div v-else>
      <router-link to="/" class="backlink">← 返回总览</router-link>

      <div class="card">
        <div class="sc-head">
          <span class="dot" :class="server.online ? 'ok' : 'bad'"></span>
          <span class="sc-name" style="font-size: 17px">{{ name }}</span>
          <button class="icon-btn" title="重命名" @click="doRename">✎</button>
          <span class="muted" style="margin-left: 10px; font-size: 12px">
            {{ server.online ? '在线' : '离线' }}
          </span>
          <span class="sc-uptime">已运行 {{ fmtDur(latest?.uptime) }}</span>
        </div>
        <div class="info-grid" style="margin-top: 10px">
          <span>真实主机名：<b>{{ server.info.hostname }}</b></span>
          <span>CPU：<b>{{ server.info.cpuModel || '—' }} · {{ server.info.cpuCores || '?' }} 线程</b></span>
          <span>内存：<b>{{ fmtBytesAbs(server.info.memTotal) }}</b></span>
          <span>系统：<b>{{ server.info.platform || '—' }}</b></span>
          <span>内核：<b>{{ server.info.kernel || '—' }}（{{ server.info.kernelArch || '?' }}）</b></span>
          <span>来源 IP：<b>{{ server.info.remoteAddr || '—' }}</b></span>
          <span>GPU：<b>{{ server.info.gpuCount ? server.info.gpuCount + ' 卡' : '无' }}</b></span>
        </div>
      </div>

      <div class="tabs">
        <div
          v-for="t in tabs"
          :key="t.id"
          class="tab"
          :class="{ active: tab === t.id }"
          @click="tab = t.id"
        >
          {{ t.label }}
        </div>
      </div>

      <!-- 实时 -->
      <div v-if="tab === 'live'">
        <div class="charts-grid">
          <div class="card chart-card"><p class="panel-title">CPU 使用率</p><Chart :option="cpuOpt" /></div>
          <div class="card chart-card"><p class="panel-title">内存使用率</p><Chart :option="memOpt" /></div>
          <div class="card chart-card"><p class="panel-title">磁盘读写（合计）</p><Chart :option="diskOpt" /></div>
          <div v-if="diskNames.length" class="card chart-card"><p class="panel-title">各磁盘读写（实线读 / 虚线写）</p><Chart :option="diskDetailOpt" /></div>
          <div class="card chart-card"><p class="panel-title">网络吞吐</p><Chart :option="netOpt" /></div>
          <div v-if="hasGPU" class="card chart-card"><p class="panel-title">GPU 利用率</p><Chart :option="gpuOpt" /></div>
        </div>

        <p class="section-title">各磁盘实时 IO</p>
        <div class="tbl-scroll" v-if="latest?.disks?.length">
          <table class="tbl">
            <thead>
              <tr><th>设备</th><th>读</th><th>写</th><th>读 IOPS</th><th>写 IOPS</th><th style="min-width:140px">繁忙度</th></tr>
            </thead>
            <tbody>
              <tr v-for="d in latest.disks" :key="d.name">
                <td>{{ d.name }}</td>
                <td>{{ fmtBytes(d.readBps) }}</td>
                <td>{{ fmtBytes(d.writeBps) }}</td>
                <td>{{ d.readIops?.toFixed(1) ?? '—' }}</td>
                <td>{{ d.writeIops?.toFixed(1) ?? '—' }}</td>
                <td>
                  <div class="bar-row" style="margin: 2px 0">
                    <div class="bar-track">
                      <div class="bar-fill" :class="utilCls(d.util)" :style="{ width: (d.util || 0) + '%' }"></div>
                    </div>
                    <span class="bar-val">{{ d.util != null ? d.util.toFixed(0) + '%' : '—' }}</span>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="muted">未采集到磁盘 IO 数据。</div>

        <p class="section-title">磁盘容量</p>
        <div class="card" v-if="latest?.mounts?.length">
          <div v-for="m in latest.mounts" :key="m.mountPoint">
            <Bar :label="shortMount(m.mountPoint)" :value="m.total ? (m.used / m.total) * 100 : null" />
            <div class="kv" style="margin: -4px 0 6px">
              <span class="k">{{ m.device }} → {{ m.mountPoint }}（{{ m.fsType }}）</span>
              <span>{{ fmtBytesAbs(m.used) }} / {{ fmtBytesAbs(m.total) }}</span>
            </div>
          </div>
        </div>

        <p class="section-title">每核心负载</p>
        <div class="card">
          <div style="display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 2px 24px">
            <div class="kv" v-for="(c, i) in latest?.cpu?.perCore || []" :key="i">
              <span class="k">核心 {{ i }}</span>
              <span>{{ c.toFixed(1) }}%</span>
            </div>
          </div>
          <div class="kv"><span class="k">系统负载 1 / 5 / 15 分钟</span><span>{{ loadStr }}</span></div>
          <div class="kv"><span class="k">CPU 温度</span><span>{{ fmtTemp(latest?.cpu?.temp) }}</span></div>
        </div>
      </div>

      <!-- GPU -->
      <div v-if="tab === 'gpu'">
        <div v-if="!latest?.gpus?.length" class="empty">
          此服务器未检测到 NVIDIA GPU（agent 配置 gpu.enable，或 nvidia-smi 不可用）。
        </div>
        <div class="gpu-grid">
          <div v-for="g in latest?.gpus || []" :key="g.uuid || g.index" class="card">
            <div class="gpu-name">GPU{{ g.index }} · {{ g.name }}</div>
            <Bar label="利用率" :value="g.util" />
            <Bar label="显存" :value="memUtilOf(g)" />
            <div class="kv"><span class="k">显存占用</span><span>{{ g.memUsed }} / {{ g.memTotal }} MiB</span></div>
            <div class="kv">
              <span class="k">温度</span>
              <span :class="g.temp >= 85 ? 'status-bad' : ''">{{ fmtTemp(g.temp) }}</span>
            </div>
            <div class="kv"><span class="k">风扇</span><span>{{ g.fan == null ? '—' : g.fan.toFixed(0) + '%' }}</span></div>
            <div class="kv">
              <span class="k">功耗</span>
              <span>
                {{ g.powerDraw == null ? '—' : g.powerDraw.toFixed(1) + ' W' }}
                {{ g.powerLimit != null ? '/ 限 ' + g.powerLimit.toFixed(0) + ' W' : '' }}
              </span>
            </div>
            <div class="kv"><span class="k">频率 SM / 显存</span><span>{{ g.clockSM.toFixed(0) }} / {{ g.clockMem.toFixed(0) }} MHz</span></div>
            <div class="kv"><span class="k">性能状态</span><span>{{ g.pState }}</span></div>
            <div class="kv">
              <span class="k">ECC（纠错 / 未纠错）</span>
              <span :class="g.eccUncorrected > 0 ? 'status-bad' : ''">{{ g.eccCorrected }} / {{ g.eccUncorrected }}</span>
            </div>
            <div v-if="g.procs?.length" style="margin-top: 8px">
              <p class="panel-title">占用进程（{{ g.procs.length }}）</p>
              <div class="tbl-scroll" style="max-height: 180px">
                <table class="tbl">
                  <thead><tr><th>PID</th><th>进程</th><th>显存</th></tr></thead>
                  <tbody>
                    <tr v-for="p in g.procs" :key="p.pid">
                      <td>{{ p.pid }}</td>
                      <td class="wrap">{{ p.name }}</td>
                      <td>{{ p.memUsed }} MiB</td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- IPMI -->
      <div v-if="tab === 'ipmi'">
        <div v-if="!latest?.ipmi" class="empty">
          此服务器未启用 IPMI 采集（agent 配置 ipmi.enable = true 且需安装 openipmi/ipmitool）。
        </div>
        <template v-else>
          <div class="card">
            <div class="kv">
              <span class="k">IPMI 状态</span>
              <span :class="latest.ipmi.available ? 'status-ok' : 'status-bad'">
                {{ latest.ipmi.available ? '正常（本地 ipmitool）' : '不可用：' + (latest.ipmi.error || '未知错误') }}
              </span>
            </div>
            <div class="kv">
              <span class="k">整机功耗</span>
              <span v-if="latest.ipmi.powerWatts" class="big-power">{{ latest.ipmi.powerWatts.toFixed(0) }} W</span>
              <span v-else>—</span>
            </div>
          </div>

          <p class="section-title">电源控制（agent → 本地 ipmitool chassis power）</p>
          <div class="card">
            <div class="power-bar">
              <button class="btn primary" :disabled="!!powerBusy" @click="power('on')">开机</button>
              <button class="btn" :disabled="!!powerBusy" @click="power('soft')">软关机 (ACPI)</button>
              <button class="btn danger" :disabled="!!powerBusy" @click="power('off')">硬断电</button>
              <button class="btn danger" :disabled="!!powerBusy" @click="power('reset')">硬重启</button>
              <button class="btn danger" :disabled="!!powerBusy" @click="power('cycle')">断电循环</button>
              <button class="btn" :disabled="!!powerBusy" @click="power('status')">查询电源状态</button>
            </div>
            <div v-if="powerResult" class="result" :class="powerResult.ok ? 'ok' : 'err'">
              {{ powerResult.text }}
            </div>
          </div>

          <p class="section-title">传感器</p>
          <div v-for="grp in sensorGroups" :key="grp.name" style="margin-bottom: 16px">
            <p class="panel-title">{{ grp.name }}（{{ grp.list.length }}）</p>
            <div class="tbl-scroll">
              <table class="tbl">
                <thead><tr><th>名称</th><th>数值</th><th>状态</th></tr></thead>
                <tbody>
                  <tr v-for="s in grp.list" :key="s.name">
                    <td>{{ s.name }}</td>
                    <td>{{ s.value }} {{ s.unit }}</td>
                    <td :class="sensorClass(s.status)">{{ s.status }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>

          <p class="section-title">SEL 事件日志（最新在前）</p>
          <div v-if="latest.ipmi.sel?.length" class="tbl-scroll">
            <table class="tbl">
              <thead><tr><th>ID</th><th>日期</th><th>时间</th><th>事件</th></tr></thead>
              <tbody>
                <tr v-for="e in [...latest.ipmi.sel].reverse()" :key="e.id">
                  <td>{{ e.id }}</td>
                  <td>{{ e.date }}</td>
                  <td>{{ e.time }}</td>
                  <td class="wrap">{{ e.event }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div v-else class="muted">无 SEL 记录。</div>
        </template>
      </div>

      <!-- 本机配置 -->
      <div v-if="tab === 'hw'">
        <div v-if="!hw" class="empty">等待硬件配置上报（agent 连接后立即上报一次）…</div>
        <div v-else class="charts-grid" style="grid-template-columns: repeat(auto-fit, minmax(380px, 1fr))">
          <div class="card">
            <p class="panel-title">系统与主板</p>
            <div class="kv"><span class="k">操作系统</span><span>{{ hw.os || '—' }}（{{ hw.arch || '?' }}）</span></div>
            <div class="kv"><span class="k">内核</span><span>{{ hw.kernel || '—' }}</span></div>
            <div class="kv"><span class="k">虚拟化环境</span><span>{{ hw.virtual || '物理机' }}</span></div>
            <div class="kv"><span class="k">整机厂商 / 型号</span><span>{{ hw.systemVendor || '—' }} {{ hw.systemVendor ? '·' + (hw.boardName || '') : hw.boardName || '' }}</span></div>
            <div class="kv"><span class="k">整机序列号</span><span>{{ hw.systemSerial || '—' }}</span></div>
            <div class="kv"><span class="k">主板</span><span>{{ hw.boardVendor || '—' }} {{ hw.boardName ? '· ' + hw.boardName : '' }}</span></div>
            <div class="kv"><span class="k">主板序列号</span><span>{{ hw.boardSerial || '—' }}</span></div>
            <div class="kv"><span class="k">BIOS</span><span>{{ hw.biosVersion || '—' }}{{ hw.biosDate ? '（' + hw.biosDate + '）' : '' }} {{ hw.biosVendor || '' }}</span></div>
            <div class="kv"><span class="k">BMC 固件</span><span>{{ hw.bmcVersion || '—' }}</span></div>
          </div>

          <div class="card">
            <p class="panel-title">处理器</p>
            <div class="kv"><span class="k">型号</span><span>{{ hw.cpuModel || '—' }}</span></div>
            <div class="kv"><span class="k">插槽 × 物理核</span><span>{{ hw.cpuSockets || '?' }} × {{ hw.cpuCoresPerSocket || '?' }}</span></div>
            <div class="kv"><span class="k">逻辑线程</span><span>{{ hw.cpuThreads || '?' }}</span></div>
            <div class="kv"><span class="k">NUMA 节点</span><span>{{ hw.cpuNumaNodes || '—' }}</span></div>
            <div class="kv"><span class="k">L3 缓存</span><span>{{ hw.cpuL3Cache || '—' }}</span></div>
            <div class="kv"><span class="k">最高频率</span><span>{{ hw.cpuMaxFreq || '—' }}</span></div>
            <div class="kv"><span class="k">虚拟化支持</span><span>{{ hw.cpuVirt || '—' }}</span></div>
          </div>

          <div class="card">
            <p class="panel-title">内存（{{ fmtBytesAbs(hw.memTotalBytes) }}{{ hw.memChannels ? ' · ' + hw.memChannels + ' 通道' : '' }}{{ hw.memSlotsPopulated ? ' · 已插 ' + hw.memSlotsPopulated + '/' + hw.memSlotsTotal + ' 条' : '' }}）</p>
            <div class="tbl-scroll" v-if="hw.memModules?.length" style="max-height: 300px">
              <table class="tbl">
                <thead><tr><th>槽位</th><th>容量</th><th>类型</th><th>速率</th><th>厂商 / 型号</th></tr></thead>
                <tbody>
                  <tr v-for="(m, i) in hw.memModules" :key="i" :style="m.populated ? '' : 'opacity:.45'">
                    <td>{{ m.locator || '—' }}</td>
                    <td>{{ m.populated ? (m.size || '—') : '空' }}</td>
                    <td>{{ m.type || '—' }}</td>
                    <td>{{ m.speed || '—' }}</td>
                    <td class="wrap">{{ m.populated ? ((m.manufacturer || '—') + ' ' + (m.partNumber || '')) : '' }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div v-else class="muted">未读取到内存条明细（需 agent 以 root 运行 dmidecode）。</div>
          </div>

          <div class="card" v-if="hw.gpus?.length">
            <p class="panel-title">显卡</p>
            <div class="kv" v-for="g in hw.gpus" :key="g.index">
              <span class="k">GPU{{ g.index }}</span>
              <span>{{ g.name }}{{ g.memoryMiB ? ' · ' + g.memoryMiB + ' MiB' : '' }}{{ g.driver ? ' · 驱动 ' + g.driver : '' }}</span>
            </div>
          </div>

          <div class="card" v-if="hw.disks?.length">
            <p class="panel-title">硬盘（{{ hw.disks.length }} 块）</p>
            <div class="tbl-scroll" style="max-height: 320px">
              <table class="tbl">
                <thead><tr><th>设备</th><th>型号</th><th>容量</th><th>接口</th><th>类型</th><th>序列号</th></tr></thead>
                <tbody>
                  <tr v-for="d in hw.disks" :key="d.name">
                    <td>{{ d.name }}</td>
                    <td class="wrap">{{ d.model || '—' }}</td>
                    <td>{{ fmtBytesAbs(d.sizeBytes) }}</td>
                    <td>{{ (d.transport || '—').toUpperCase() }}</td>
                    <td>{{ d.rotational ? 'HDD 机械' : 'SSD 固态' }}</td>
                    <td>{{ d.serial || '—' }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>

          <div class="card" v-if="hw.nics?.length">
            <p class="panel-title">网卡（{{ hw.nics.length }}）</p>
            <div class="kv" v-for="n in hw.nics" :key="n.address">
              <span class="k">{{ n.address }}</span>
              <span class="wrap">{{ n.model }}</span>
            </div>
          </div>
        </div>
        <p class="muted" style="margin-top: 10px; font-size: 12px">
          采集时间：{{ fmtLogTs(hw.collectedAt) }} · 每 30 分钟自动刷新（可通过 agent 配置 hw.intervalMin 调整）
        </p>
      </div>

      <!-- 日志 -->
      <div v-if="tab === 'logs'">
        <div class="range-bar">
          <button
            v-for="r in logsRanges"
            :key="r.sec"
            class="range-btn"
            :class="{ active: logsRangeSec === r.sec }"
            @click="logsRangeSec = r.sec"
          >
            {{ r.label }}
          </button>
          <span class="muted" style="align-self: center; margin-left: 8px; font-size: 12px">
            来源：journalctl 严重级（err 及以上）——内核崩溃 / 硬件错误（MCE·EDAC·PCIe AER）/ 服务故障；实时推送 + 数据库回看（保留 7 天）
          </span>
        </div>
        <div v-if="mergedLogs.length" class="tbl-scroll" style="max-height: 560px">
          <table class="tbl">
            <thead>
              <tr><th>时间</th><th>级别</th><th>来源</th><th>单元</th><th>消息</th></tr>
            </thead>
            <tbody>
              <tr v-for="(e, i) in mergedLogs" :key="i">
                <td>{{ fmtLogTs(e.ts) }}</td>
                <td :class="prioCls(e.prio)">{{ prioLabel(e.prio) }}</td>
                <td>{{ e.source }}</td>
                <td>{{ e.unit || '—' }}</td>
                <td class="wrap">{{ e.msg }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="empty">
          该时段无严重日志记录（这是好事）
          <div class="muted" style="margin-top: 12px; font-size: 13px">
            采集来源：Ubuntu 用 journalctl（err 级以上）；Windows 用系统事件日志（System 通道 错误/严重级）。
          </div>
        </div>
      </div>

      <!-- 历史 -->
      <div v-if="tab === 'history'">
        <HistoryView :server="name" />
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, defineAsyncComponent, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { state } from '../store.js'
import { fmtBytes, fmtBytesAbs, fmtDur, fmtTemp, getLogs, postPower, postRename } from '../api.js'
import { lineOption } from '../charts.js'
import Bar from '../components/Bar.vue'
import Chart from '../components/Chart.vue'
const HistoryView = defineAsyncComponent(() => import('../components/HistoryView.vue'))

const props = defineProps({ name: { type: String, required: true } })
const router = useRouter()

async function doRename() {
  const input = prompt('修改服务器显示名称（页面与告警邮件都将使用新名称）', props.name)
  if (input == null) return
  const name = input.trim()
  if (!name || name === props.name) return
  try {
    await postRename(props.name, name)
    router.replace('/s/' + encodeURIComponent(name))
  } catch (e) {
    alert('重命名失败：' + e.message)
  }
}

const tabs = [
  { id: 'live', label: '实时' },
  { id: 'gpu', label: 'GPU' },
  { id: 'ipmi', label: 'IPMI' },
  { id: 'logs', label: '日志' },
  { id: 'hw', label: '本机配置' },
  { id: 'history', label: '历史' },
]
const tab = ref('live')

const server = computed(() => state.servers[props.name])
const latest = computed(() => server.value?.latest)
const hw = computed(() => server.value?.config)
const ring = computed(() => server.value?.ring || [])
const hasGPU = computed(() => ring.value.some((p) => p.gpuUtil != null))

const cpuOpt = computed(() =>
  lineOption({
    yFmt: (v) => v.toFixed(0) + '%',
    max: 100,
    series: [
      { name: '使用率', color: '#4cc2ff', area: true, data: ring.value.map((p) => [p.t * 1000, p.cpu]) },
      { name: 'iowait', color: '#d29922', data: ring.value.map((p) => [p.t * 1000, p.iowait]) },
    ],
  }),
)
const memOpt = computed(() =>
  lineOption({
    yFmt: (v) => v.toFixed(0) + '%',
    max: 100,
    series: [
      { name: '内存使用率', color: '#a371f7', area: true, data: ring.value.map((p) => [p.t * 1000, p.memPct]) },
    ],
  }),
)
const diskOpt = computed(() =>
  lineOption({
    yFmt: (v) => fmtBytes(v),
    series: [
      { name: '读', color: '#3fb950', data: ring.value.map((p) => [p.t * 1000, p.diskR]) },
      { name: '写', color: '#f0883e', data: ring.value.map((p) => [p.t * 1000, p.diskW]) },
    ],
  }),
)
const diskPalette = ['#4cc2ff', '#3fb950', '#f0883e', '#a371f7', '#e599f7', '#d29922', '#58a6ff', '#ff7b72']
const diskRingMap = computed(() => server.value?.diskRing || {})
const diskNames = computed(() => Object.keys(diskRingMap.value).sort())
const diskDetailOpt = computed(() => {
  const series = []
  diskNames.value.forEach((name, i) => {
    const color = diskPalette[i % diskPalette.length]
    const pts = diskRingMap.value[name] || []
    series.push({ name: name + ' 读', color, data: pts.map((p) => [p.t * 1000, p.r]) })
    series.push({ name: name + ' 写', color, dashed: true, data: pts.map((p) => [p.t * 1000, p.w]) })
  })
  return lineOption({ yFmt: (v) => fmtBytes(v), series })
})
function utilCls(v) {
  if (v == null) return ''
  return v >= 90 ? 'crit' : v >= 70 ? 'warn' : ''
}
const netOpt = computed(() =>
  lineOption({
    yFmt: (v) => fmtBytes(v),
    series: [
      { name: '下行', color: '#4cc2ff', area: true, data: ring.value.map((p) => [p.t * 1000, p.netRx]) },
      { name: '上行', color: '#e599f7', data: ring.value.map((p) => [p.t * 1000, p.netTx]) },
    ],
  }),
)
const gpuOpt = computed(() =>
  lineOption({
    yFmt: (v) => v.toFixed(0) + '%',
    max: 100,
    series: [
      { name: 'GPU 利用率(均值)', color: '#3fb950', area: true, data: ring.value.map((p) => [p.t * 1000, p.gpuUtil]) },
    ],
  }),
)

const loadStr = computed(() => {
  const l = latest.value?.load
  if (!l) return '—'
  return `${l.load1.toFixed(2)} / ${l.load5.toFixed(2)} / ${l.load15.toFixed(2)}`
})

// ---- severe logs tab ----
const logsRanges = [
  { label: '1小时', sec: 3600 },
  { label: '6小时', sec: 21600 },
  { label: '24小时', sec: 86400 },
  { label: '7天', sec: 604800 },
]
const logsRangeSec = ref(86400)
const dbLogs = ref([])
const logsLoaded = ref(false)

async function loadLogs() {
  const to = Math.floor(Date.now() / 1000)
  const from = to - logsRangeSec.value
  try {
    const r = await getLogs(props.name, from, to, 500)
    dbLogs.value = r.entries || []
  } catch {
    dbLogs.value = []
  }
}
watch(
  () => tab.value,
  (t) => {
    if (t === 'logs' && !logsLoaded.value) {
      logsLoaded.value = true
      loadLogs()
    }
  },
)
watch(logsRangeSec, () => {
  logsLoaded.value = true
  loadLogs()
})

const mergedLogs = computed(() => {
  const key = (e) => e.ts + '|' + (e.unit || '') + '|' + (e.msg || '')
  const map = new Map()
  for (const e of dbLogs.value) map.set(key(e), e)
  for (const e of server.value?.logs || []) map.set(key(e), e)
  return [...map.values()].sort((a, b) => b.ts - a.ts).slice(0, 300)
})

function fmtLogTs(ts) {
  return new Date(ts * 1000).toLocaleString('zh-CN', { hour12: false })
}
function prioLabel(p) {
  return { 0: '紧急', 1: '警报', 2: '严重', 3: '错误' }[p] || 'P' + p
}
function prioCls(p) {
  return p <= 2 ? 'status-bad' : 'status-warn'
}

function shortMount(mp) {
  if (mp === '/') return '/'
  const parts = mp.split('/').filter(Boolean)
  return parts.length ? parts[parts.length - 1].slice(0, 8) : mp
}
function memUtilOf(g) {
  if (g.memUtil) return g.memUtil
  return g.memTotal ? (g.memUsed / g.memTotal) * 100 : null
}
function sensorClass(st) {
  if (st === 'ok') return 'status-ok'
  if (!st || st === 'na' || st === 'ns') return 'muted'
  return 'status-bad'
}

// ---- IPMI power control ----
const powerBusy = ref('')
const powerResult = ref(null)
const powerLabels = {
  on: '开机', off: '硬断电', soft: '软关机 (ACPI)',
  cycle: '断电循环', reset: '硬重启', status: '查询电源状态',
}
async function power(action) {
  if (['off', 'soft', 'cycle', 'reset'].includes(action)) {
    if (!confirm(`确认对 ${props.name} 执行「${powerLabels[action]}」？`)) return
  }
  powerBusy.value = action
  powerResult.value = null
  try {
    const r = await postPower(props.name, action)
    powerResult.value = { ok: !!r.ok, text: (r.ok ? '✓ ' : '✗ ') + (r.output || '(无输出)') }
  } catch (e) {
    powerResult.value = { ok: false, text: '✗ ' + e.message }
  } finally {
    powerBusy.value = ''
  }
}

const sensorGroups = computed(() => {
  const ipmi = latest.value?.ipmi
  if (!ipmi?.sensors) return []
  const groups = {}
  for (const s of ipmi.sensors) {
    const u = (s.unit || '').toLowerCase()
    let g = '其他'
    if (u.includes('deg') || u.includes('celsius')) g = '温度'
    else if (u.includes('rpm')) g = '风扇'
    else if (u.includes('volt')) g = '电压'
    else if (u.includes('watt')) g = '功率'
    else if (u.includes('amp')) g = '电流'
    ;(groups[g] = groups[g] || []).push(s)
  }
  const order = ['温度', '风扇', '电压', '功率', '电流', '其他']
  return Object.entries(groups)
    .sort((a, b) => order.indexOf(a[0]) - order.indexOf(b[0]))
    .map(([name, list]) => ({ name, list }))
})
</script>
