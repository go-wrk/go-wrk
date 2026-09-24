<script setup lang="ts">
// 柱状图。延迟分布用它 —— 桶本身是对数分桶，按索引等距画出来横轴就是对数刻度，
// 低延迟那一头不会被挤成一团。
// 鼠标移上去按横坐标定位到最近的那根柱子，显示它在 tip 里准备好的说明。
import { computed, ref } from "vue"
import { useElementWidth } from "../useElementWidth"
import type { Bar } from "../types"

const props = withDefaults(
  defineProps<{
    bars: Bar[]
    height?: number
    color?: string
  }>(),
  { height: 130, color: "#60a5fa" },
)

const { el, width } = useElementWidth()

const PAD = { top: 10, right: 12, bottom: 20, left: 44 }

const innerW = computed(() => Math.max(10, width.value - PAD.left - PAD.right))
const innerH = computed(() => Math.max(10, props.height - PAD.top - PAD.bottom))

const maxValue = computed(() => {
  let max = 0
  for (const b of props.bars) {
    if (b.value > max) max = b.value
  }
  return max || 1
})

const barW = computed(() => (props.bars.length ? innerW.value / props.bars.length : 0))

const bars = computed(() =>
  props.bars.map((b, i) => {
    const h = (b.value / maxValue.value) * innerH.value
    // 有值但极矮的柱子至少给 1px，否则看起来像没数据
    const height = b.value > 0 ? Math.max(1, h) : 0
    return {
      x: PAD.left + i * barW.value,
      y: PAD.top + innerH.value - height,
      w: Math.max(1, barW.value - 1),
      h: height,
    }
  }),
)

/** Y 轴刻度统一用几位小数，免得出现 0.00 这种。 */
const yDigits = computed(() => {
  const step = maxValue.value / 2
  if (step >= 10) return 0
  if (step >= 1) return Number.isInteger(step) ? 0 : 1
  if (step >= 0.1) return 1
  return 2
})

const yTicks = computed(() => {
  const max = maxValue.value
  return [0, max / 2, max].map((v) => ({
    v,
    y: PAD.top + innerH.value - (v / max) * innerH.value,
  }))
})

/** X 轴只标几个刻度，全标会糊成一片。 */
const xTicks = computed(() => {
  const n = props.bars.length
  if (n === 0) return []
  const out: { x: number; label: string }[] = []
  const seen = new Set<number>()
  for (const i of [0, Math.floor(n / 2), n - 1]) {
    // 桶只有一两个时这几个下标会撞在一起，去重免得同一处画两三遍
    if (seen.has(i)) continue
    seen.add(i)
    out.push({ x: PAD.left + (i + 0.5) * barW.value, label: props.bars[i].label })
  }
  return out
})

function fmt(v: number): string {
  if (v === 0) return "0"
  if (v >= 1e6) return (v / 1e6).toFixed(1) + "M"
  if (v >= 1000) return (v / 1000).toFixed(1) + "k"
  return v.toFixed(yDigits.value)
}

// ---- 悬停 ----

const hover = ref<number | null>(null)

/** 按鼠标横坐标算出落在第几根柱子上。柱子只有几像素宽，逐根挂事件不好点。 */
function onMove(e: MouseEvent) {
  const step = barW.value
  if (step <= 0) return
  const box = (e.currentTarget as SVGElement).getBoundingClientRect()
  const i = Math.floor((e.clientX - box.left - PAD.left) / step)
  hover.value = i >= 0 && i < props.bars.length ? i : null
}

const hoverTip = computed(() => {
  const i = hover.value
  if (i === null) return []
  const b = props.bars[i]
  // 数据变短时这个下标可能已经越界（鼠标没动过，mouseleave 就不会触发）
  if (!b) return []
  return b.tip ?? [b.label, String(b.value)]
})

/** 提示框贴着图边会被切掉，往里收一点。 */
const tipLeft = computed(() => {
  const half = 80
  const at = PAD.left + ((hover.value ?? 0) + 0.5) * barW.value
  return Math.min(Math.max(at, half), Math.max(half, width.value - half))
})
</script>

<template>
  <div ref="el" class="chart-wrap" :style="{ height: height + 'px' }">
    <svg :width="width" :height="height" @mousemove="onMove" @mouseleave="hover = null">
      <g class="grid">
        <line
          v-for="t in yTicks"
          :key="'y' + t.v"
          :x1="PAD.left"
          :x2="width - PAD.right"
          :y1="t.y"
          :y2="t.y"
        />
      </g>
      <g class="axis-label">
        <text v-for="t in yTicks" :key="'yl' + t.v" :x="PAD.left - 6" :y="t.y + 3" text-anchor="end">
          {{ fmt(t.v) }}
        </text>
        <text v-for="(t, i) in xTicks" :key="'xl' + i" :x="t.x" :y="height - 5" text-anchor="middle">
          {{ t.label }}
        </text>
      </g>

      <rect
        v-for="(b, i) in bars"
        :key="i"
        :x="b.x"
        :y="b.y"
        :width="b.w"
        :height="b.h"
        :fill="color"
        :opacity="hover === i ? 1 : 0.85"
      />

      <text v-if="!bars.length" :x="width / 2" :y="height / 2" text-anchor="middle" class="empty">
        无数据
      </text>
    </svg>

    <div v-if="hoverTip.length" class="tip" :style="{ left: tipLeft + 'px' }">
      <div v-for="(line, i) in hoverTip" :key="i" :class="{ head: i === 0 }">{{ line }}</div>
    </div>
  </div>
</template>

<style scoped>
.chart-wrap {
  position: relative;
  width: 100%;
}

svg {
  display: block;
}

.grid line {
  stroke: rgba(255, 255, 255, 0.07);
  stroke-width: 1;
}

.axis-label text {
  fill: #6b7f9e;
  font-size: 10px;
  font-variant-numeric: tabular-nums;
}

.empty {
  fill: #4a5a75;
  font-size: 12px;
}

.tip {
  position: absolute;
  top: 2px;
  transform: translateX(-50%);
  pointer-events: none;
  z-index: 2;
  padding: 5px 8px;
  background: rgba(14, 21, 32, 0.96);
  border: 1px solid #2b3a52;
  border-radius: 6px;
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  color: #cbd5e1;
}

.tip .head {
  color: #e8eef7;
  margin-bottom: 2px;
}
</style>
