// 前端共享类型和格式化函数。
//
// Config / Snapshot 这些结构类型不在这里手写，直接用 Wails 从 Go 结构生成的
// （frontend/wailsjs/go/models.ts）。手抄一份的话，Go 加了字段前端不会报错，
// 只会静默漏掉；用别名就让编译器替我们盯着这件事。
//
// Line 和 Bar 是纯前端的，Go 侧没有对应结构，只能写在这。

import type { main } from "../wailsjs/go/models"

export type Config = main.Config
export type NamedConfig = main.NamedConfig
export type RequestRecord = main.RequestRecord
export type LatencyBin = main.LatencyBin
export type Snapshot = main.Snapshot

/** 折线图上的一条线。x 是秒，y 是数值。 */
export interface Line {
  name: string
  color: string
  points: { x: number; y: number }[]
}

/** 柱状图的一根柱子。 */
export interface Bar {
  label: string
  value: number
  /** 悬停提示里逐行显示的内容；不给就只显示标签和数值。 */
  tip?: string[]
}

/** 把毫秒数格式化成好读的字符串。 */
export function formatMs(ms: number): string {
  if (ms <= 0) return "0"
  if (ms < 1) return ms.toFixed(2) + "ms"
  if (ms < 10) return ms.toFixed(1) + "ms"
  if (ms < 1000) return ms.toFixed(0) + "ms"
  return (ms / 1000).toFixed(2) + "s"
}

/** 把字节数格式化成好读的字符串。 */
export function formatBytes(bytes: number): string {
  // 先取整再分档：这个函数会被除法喂小数进来（总字节 ÷ 请求数、字节 ÷ 秒），
  // 不取整的话第一档会直接拼出「166.66666666666666 B」这种一串
  const n = Math.round(bytes)
  if (n < 1024) return n + " B"
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB"
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + " MB"
  return (n / 1024 / 1024 / 1024).toFixed(2) + " GB"
}

/** 每秒字节数，比如「1.2 MB/s」。 */
export function formatRate(bytes: number, sec: number): string {
  if (sec <= 0) return "—"
  return formatBytes(bytes / sec) + "/s"
}

/** 延迟区间的端点。比 formatMs 细一档 —— 对数桶在近处相邻下界只差零点几毫秒。 */
export function formatEdge(ms: number): string {
  if (ms <= 0) return "0"
  if (ms < 0.01) return (ms * 1000).toFixed(0) + "μs"
  if (ms < 10) return ms.toFixed(2) + "ms"
  if (ms < 100) return ms.toFixed(1) + "ms"
  if (ms < 1000) return ms.toFixed(0) + "ms"
  return (ms / 1000).toFixed(2) + "s"
}

/** 把秒数说成「X分Y秒Z毫秒」。毫秒固定补足三位 —— 压测中它一直在变，
    不补零的话卡片里那个数字会左右抖。 */
export function formatDuration(sec: number): string {
  const total = Math.max(0, Math.round(sec * 1000))
  const min = Math.floor(total / 60000)
  const s = Math.floor((total % 60000) / 1000)
  const ms = String(total % 1000).padStart(3, "0")
  return `${min}分${s}秒${ms}毫秒`
}

/** 把 Unix 毫秒格式化成「年月日 时分秒.毫秒」。 */
export function formatClock(ms: number): string {
  if (!ms) return "—"
  const d = new Date(ms)
  const two = (n: number) => String(n).padStart(2, "0")
  const date = `${d.getFullYear()}-${two(d.getMonth() + 1)}-${two(d.getDate())}`
  const time = `${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`
  return `${date} ${time}.${String(d.getMilliseconds()).padStart(3, "0")}`
}

/** 带千分位的整数，比如 26049 → "26,049"。要看准数的场合用它，别用缩写。 */
export function formatInt(n: number): string {
  return Math.round(n).toLocaleString()
}

/** 把大数字压缩成带单位的短字符串。 */
export function formatCount(n: number): string {
  if (n < 1000) return String(n)
  if (n < 1e6) return (n / 1000).toFixed(1) + "k"
  return (n / 1e6).toFixed(2) + "M"
}
