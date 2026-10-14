// Shared ECharts option builders (dark theme tweaks).
export function lineOption({ series, yFmt, max, min }) {
  const fmt = yFmt || ((v) => v)
  return {
    animation: false,
    backgroundColor: 'transparent',
    grid: { left: 10, right: 14, top: 30, bottom: 8, containLabel: true },
    tooltip: {
      trigger: 'axis',
      backgroundColor: '#161d26',
      borderColor: '#1f2937',
      textStyle: { color: '#d5dee8', fontSize: 12 },
      valueFormatter: (v) => (v == null ? '—' : fmt(v)),
    },
    legend: {
      top: 0,
      right: 0,
      icon: 'rect',
      itemWidth: 10,
      itemHeight: 3,
      textStyle: { color: '#9fb0c3', fontSize: 11 },
    },
    xAxis: {
      type: 'time',
      axisLabel: { color: '#7b8a9c', hideOverlap: true, fontSize: 10 },
      axisLine: { lineStyle: { color: '#1f2937' } },
      splitLine: { show: false },
    },
    yAxis: {
      type: 'value',
      max,
      min,
      axisLabel: { color: '#7b8a9c', fontSize: 10, formatter: (v) => fmt(v) },
      splitLine: { lineStyle: { color: 'rgba(255,255,255,0.05)' } },
    },
    series: series.map((s) => ({
      name: s.name,
      type: 'line',
      showSymbol: false,
      lineStyle: { width: 1.5, color: s.color, type: s.lineType || (s.dashed ? 'dashed' : 'solid'), opacity: s.dim ? 0.55 : 1 },
      itemStyle: { color: s.color, opacity: s.dim ? 0.55 : 1 },
      areaStyle: s.area ? { opacity: 0.12, color: s.color } : undefined,
      data: s.data,
      connectNulls: false,
    })),
  }
}
