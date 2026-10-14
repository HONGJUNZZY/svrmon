<template>
  <div class="bar-row">
    <span class="bar-label">{{ label }}</span>
    <div class="bar-track">
      <div class="bar-fill" :class="cls" :style="{ width: (pct ?? 0) + '%' }"></div>
    </div>
    <span class="bar-val">{{ pct == null ? '—' : pct.toFixed(0) + '%' }}</span>
  </div>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  label: { type: String, required: true },
  value: { type: Number, default: null },
})

const pct = computed(() =>
  props.value == null || isNaN(props.value) ? null : Math.max(0, Math.min(100, props.value)),
)
const cls = computed(() =>
  pct.value == null ? '' : pct.value >= 90 ? 'crit' : pct.value >= 70 ? 'warn' : '',
)
</script>
