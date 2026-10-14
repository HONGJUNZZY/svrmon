<template>
  <div ref="el" class="chart"></div>
</template>

<script setup>
import * as echarts from 'echarts'
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

const props = defineProps({
  option: { type: Object, required: true },
})

const el = ref(null)
let chart = null
let ro = null

function render() {
  if (chart) chart.setOption(props.option)
}

onMounted(() => {
  chart = echarts.init(el.value, 'dark')
  chart.setOption({ backgroundColor: 'transparent' })
  render()
  ro = new ResizeObserver(() => chart && chart.resize())
  ro.observe(el.value)
})

watch(() => props.option, render, { deep: true })

onBeforeUnmount(() => {
  if (ro) ro.disconnect()
  if (chart) chart.dispose()
  chart = null
})
</script>
