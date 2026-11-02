<template>
  <div>
    <div v-if="serverCount === 0" class="empty">
      暂无服务器数据
      <div class="muted" style="margin-top:12px;font-size:13px">
        请确认各服务器的 agent 已启动并使用正确的 token 连接到 hub。
      </div>
    </div>
    <div class="grid">
      <router-link
        v-for="(s, sname) in servers"
        :key="sname"
        class="card server-card"
        :class="{ offline: !s.online }"
        :to="'/s/' + encodeURIComponent(sname)"
      >
        <div class="sc-head">
          <span class="dot" :class="s.online ? 'ok' : 'bad'"></span>
          <span class="sc-name">{{ sname }}</span>
          <button
            class="icon-btn"
            title="重命名（告警通知将使用新名称）"
            @click.prevent.stop="rename(sname)"
          >✎</button>
          <span class="sc-uptime">{{ s.online ? '已运行 ' + fmtDur(s.latest?.uptime) : '离线' }}</span>
        </div>
        <div class="bars">
          <Bar label="CPU" :value="s.latest?.cpu?.usage" />
          <Bar label="内存" :value="memPct(s)" />
          <div v-for="g in sortedGPUs(s)" :key="gpuKey(g)" class="overview-gpu">
            <div class="overview-gpu-head"><span>{{ gpuLabel(g) }}</span><span>{{ fmtTemp(g.temp) }} · {{ g.powerDraw == null ? '功耗未知' : g.powerDraw.toFixed(1) + ' W' }}</span></div>
            <Bar label="利用率" :value="g.util" />
            <Bar label="显存占用" :value="gpuMemoryPercent(g)" />
          </div>
        </div>
        <div class="sc-foot">
          <span v-if="s.latest?.cpu?.temp">CPU {{ fmtTemp(s.latest.cpu.temp) }}</span>
          <span v-if="hasSystemPower(s.latest?.ipmi)" class="pwr" :title="powerSourceLabel(s.latest.ipmi)">
            整机 {{ s.latest.ipmi.powerWatts.toFixed(0) }} W
          </span>
          <span class="net">↓{{ fmtBytes(sumNet(s.latest, 'rxBps')) }} ↑{{ fmtBytes(sumNet(s.latest, 'txBps')) }}</span>
        </div>
      </router-link>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { state } from '../store.js'
import { fmtBytes, fmtDur, fmtTemp, postRename } from '../api.js'
import Bar from '../components/Bar.vue'
import { gpuKey, gpuLabel, gpuMemoryPercent } from '../gpu.js'
import { hasSystemPower, powerSourceLabel } from '../power.js'

const servers = computed(() => state.servers)
const serverCount = computed(() => Object.keys(state.servers).length)

function memPct(s) {
  return s.latest?.mem && s.latest.mem.total
    ? (s.latest.mem.used / s.latest.mem.total) * 100
    : null
}

const sortedGPUs = (server) => [...(server.latest?.gpus || [])].sort((a, b) => a.index - b.index)

function sumNet(latest, key) {
  let v = 0
  for (const n of latest?.nets || []) v += n[key] || 0
  return v
}

async function rename(sname) {
  const input = prompt('修改服务器显示名称（页面与告警邮件都将使用新名称）', sname)
  if (input == null) return
  const name = input.trim()
  if (!name || name === sname) return
  try {
    await postRename(sname, name)
  } catch (e) {
    alert('重命名失败：' + e.message)
  }
}
</script>
