<template>
  <div class="app">
    <header class="topbar">
      <router-link to="/" class="brand">▤ svrmon<span class="sub">局域网服务器监控</span></router-link>
      <div class="conn" :class="connected ? 'ok' : 'bad'">
        {{ connected ? '● 实时连接' : '○ 连接断开，重连中…' }}
      </div>
      <div class="srv-count">
        <span v-if="hubVer" class="muted" style="margin-right: 14px">v{{ hubVer }}</span>
        {{ onlineCount }} / {{ totalCount }} 台在线
      </div>
    </header>
    <main class="main">
      <router-view />
    </main>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { state } from './store.js'

const connected = computed(() => state.connected)
const hubVer = computed(() => state.hubVer)
const totalCount = computed(() => Object.keys(state.servers).length)
const onlineCount = computed(
  () => Object.values(state.servers).filter((s) => s.online).length,
)
</script>
