<template>
  <div ref="el" class="chart"></div>
</template>

<script setup>
import * as echarts from 'echarts/core'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent, AxisPointerComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

const props = defineProps({
  option: { type: Object, required: true },
})

echarts.use([LineChart, GridComponent, TooltipComponent, LegendComponent, AxisPointerComponent, CanvasRenderer])

const el = ref(null)
let chart = null
let ro = null

function render() {
  if (chart) chart.setOption(props.option, { lazyUpdate: true })
}

onMounted(() => {
  chart = echarts.init(el.value, 'dark')
  chart.setOption({ backgroundColor: 'transparent' })
  render()
  ro = new ResizeObserver(() => chart && chart.resize())
  ro.observe(el.value)
})

watch(() => props.option, render)

onBeforeUnmount(() => {
  if (ro) ro.disconnect()
  if (chart) chart.dispose()
  chart = null
})
</script>
