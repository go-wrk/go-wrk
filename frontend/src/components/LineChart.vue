<script setup lang="ts">
// 实时折线图。X 轴是压测已运行的时间，Y 轴自适应到当前数据的最大值。
// 鼠标移上去会显示一条竖直参考线和那一刻的数值。
import { computed, ref } from "vue"
import { useElementWidth } from "../useElementWidth"
import type { Line } from "../types"

const props = withDefaults(
  defineProps<{
    lines: Line[]
    height?: number
    /** 压测开始时的墙钟时间（Unix 毫秒）。给了就在 X 轴和提示里显示真实时刻。 */
    startMs?: number
    /** 数值显示成什么样，默认保留一位小数。 */
    format?: (v: number) => string
  }>(),
  {
    height: 190,
    startMs: 0,
    format: (v: number) => v.toFixed(1),
  },
)

const { el, width } = useElementWidth()

const PAD = { top: 12, right: 14, bottom: 20, left: 50 }

const innerW = computed(() => Math.max(10, width.value - PAD.left - PAD.right))
const innerH = computed(() => Math.max(10, props.height - PAD.top - PAD.bottom))

/** 把所有线的最大值向上取到一个「整」数，作为 Y 轴上限。 */
const yMax = computed(() => {
  let max = 0
  for (const line of props.lines) {
    for (const p of line.points) {
      if (p.y > max) max = p.y
    }
  }
  if (max <= 0) return 1
  const exp = Math.floor(Math.log10(max))
  const base = Math.pow(10, exp)
  const n = max / base
  const step = n <= 1 ? 1 : n <= 2 ? 2 : n <= 5 ? 5 : 10
  return step * base
})

/** X 轴上限，至少 1 秒，免得刚起步时曲线横跳。 */
const xMax = computed(() => {
  let max = 0
  for (const line of props.lines) {
    for (const p of line.points) {
      if (p.x > max) max = p.x
    }
  }
  return Math.max(1, max)
})

const sx = (x: number) => PAD.left + (x / xMax.value) * innerW.value
const sy = (y: number) => PAD.top + innerH.value - (y / yMax.value) * innerH.value

const pathOf = (points: { x: number; y: number }[]) =>
  points.map((p, i) => `${i === 0 ? "M" : "L"}${sx(p.x).toFixed(1)},${sy(p.y).toFixed(1)}`).join(" ")

/**
 * 把「相对开始的秒数」换算成时刻。没给起点就退回相对秒数。
 *
 * 跨度只有几秒时秒级精度不够分：5 个刻度全落在同一秒里，标签看起来一模一样，
 * 像是重叠。这时候补一位小数（十分之一秒）。
 */
function clockLabel(sec: number, span: number): string {
  if (!props.startMs) return sec.toFixed(0) + "s"
  const d = new Date(props.startMs + sec * 1000)
  const two = (n: number) => String(n).padStart(2, "0")
  const hms = `${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`
  if (span >= 10) return hms
  // 补满毫秒：位数固定，相邻刻度分得开，宽度也不会左右跳
  return `${hms}.${String(d.getMilliseconds()).padStart(3, "0")}`
}

const yTicks = computed(() => {
  const max = yMax.value
  return [0, max / 4, max / 2, (max * 3) / 4, max].map((v) => ({ v, y: sy(v) }))
})

const xTicks = computed(() => {
  const max = xMax.value
  // 按可用宽度定能放几个刻度：标签长的时候少放几个，免得挤在一起
  const need = clockLabel(0, max).length * 7 + 18
  const count = Math.max(2, Math.min(5, Math.floor(innerW.value / need)))
  const out: { x: number; label: string }[] = []
  let last = ""
  for (let i = 0; i < count; i++) {
    const v = (max * i) / (count - 1)
    const label = clockLabel(v, max)
    // 还没跑过压测时 X 轴只有 1 秒跨度，几个刻度会算出一样的标签
    if (label === last) continue
    last = label
    out.push({ x: sx(v), label })
  }
  return out
})

/** Y 轴刻度统一用几位小数。跟着刻度间距走，免得出现 0.000、2.50 这种。 */
const yDigits = computed(() => {
  const step = yMax.value / 4
  if (step >= 10) return 0
  if (step >= 1) return Number.isInteger(step) ? 0 : 1
  if (step >= 0.1) return 1
  if (step >= 0.01) return 2
  return 3
})

function fmt(v: number): string {
  if (v === 0) return "0"
  if (v >= 1e6) return (v / 1e6).toFixed(1) + "M"
  if (v >= 1000) return (v / 1000).toFixed(1) + "k"
  return v.toFixed(yDigits.value)
}

// ---- 悬停 ----

const hover = ref<{
  /** 时间值（秒），不是像素 —— 压测中图会变宽，存像素的话十字线会越漂越偏 */
  at: number
  label: string
  rows: { name: string; color: string; text: string }[]
} | null>(null)

/** 点的 x 是递增的，二分找离 xVal 最近的那个。 */
function nearestIndex(pts: { x: number; y: number }[], xVal: number): number {
  let lo = 0
  let hi = pts.length - 1
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1
    if (pts[mid].x < xVal) lo = mid
    else hi = mid
  }
  return xVal - pts[lo].x <= pts[hi].x - xVal ? lo : hi
}

function onMove(e: MouseEvent) {
  const box = (e.currentTarget as SVGElement).getBoundingClientRect()
  const near = props.lines[0]?.points ?? []
  if (!near.length) {
    hover.value = null
    return
  }

  const raw = ((e.clientX - box.left - PAD.left) / innerW.value) * xMax.value
  const idx = nearestIndex(near, Math.min(Math.max(raw, 0), xMax.value))
  const at = near[idx].x

  hover.value = {
    at,
    label: clockLabel(at, xMax.value),
    rows: props.lines.map((line) => ({
      name: line.name,
      color: line.color,
      text: props.format(line.points[idx]?.y ?? 0),
    })),
  }
}

function onLeave() {
  hover.value = null
}

/** 提示框贴着图边会被切掉，往里收一点。 */
const tipLeft = computed(() => {
  const half = 64
  const at = hover.value ? sx(hover.value.at) : 0
  return Math.min(Math.max(at, half), Math.max(half, width.value - half))
})
</script>

<template>
  <div ref="el" class="chart-wrap" :style="{ height: height + 'px' }">
    <svg :width="width" :height="height" @mousemove="onMove" @mouseleave="onLeave">
      <!-- 横向网格和 Y 轴刻度 -->
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

      <!-- 悬停时的竖直参考线 -->
      <line
        v-if="hover"
        class="crosshair"
        :x1="sx(hover.at)"
        :x2="sx(hover.at)"
        :y1="PAD.top"
        :y2="height - PAD.bottom"
      />

      <!-- 数据线 -->
      <path
        v-for="line in lines"
        :key="line.name"
        :d="pathOf(line.points)"
        :stroke="line.color"
        fill="none"
        stroke-width="1.6"
        stroke-linejoin="round"
        stroke-linecap="round"
      />

      <!-- 空态 -->
      <text v-if="!lines.some((l) => l.points.length)" :x="width / 2" :y="height / 2" text-anchor="middle" class="empty">
        无数据
      </text>
    </svg>

    <div v-if="hover" class="tip" :style="{ left: tipLeft + 'px' }">
      <div class="tip-time">{{ hover.label }}</div>
      <div v-for="r in hover.rows" :key="r.name" class="tip-row">
        <i :style="{ background: r.color }"></i>
        <span>{{ r.name }}</span>
        <b>{{ r.text }}</b>
      </div>
    </div>

    <div class="legend">
      <span v-for="line in lines" :key="line.name">
        <i :style="{ background: line.color }"></i>{{ line.name }}
      </span>
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

.crosshair {
  stroke: rgba(255, 255, 255, 0.28);
  stroke-width: 1;
  stroke-dasharray: 3 3;
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

.legend {
  position: absolute;
  top: 0;
  right: 14px;
  display: flex;
  gap: 12px;
  font-size: 11px;
  color: #8fa3c0;
}

.legend i {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 2px;
  margin-right: 4px;
  vertical-align: middle;
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
}

.tip-time {
  color: #7f92ae;
  margin-bottom: 3px;
}

.tip-row {
  display: flex;
  align-items: center;
  gap: 6px;
}

.tip-row i {
  width: 7px;
  height: 7px;
  border-radius: 2px;
  flex: none;
}

.tip-row span {
  color: #8fa3c0;
}

.tip-row b {
  margin-left: auto;
  padding-left: 12px;
  font-weight: 600;
  color: #e8eef7;
}
</style>
