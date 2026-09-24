<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue"
import {
  DefaultConfig,
  DeleteConfig,
  ExportConfig,
  GetSnapshot,
  ImportConfig,
  ListConfigs,
  RecentRequests,
  SaveConfig,
  StartBench,
  StopBench,
} from "../wailsjs/go/main/App"
import { EventsOn } from "../wailsjs/runtime/runtime"
import type { main } from "../wailsjs/go/models"
import LineChart from "./components/LineChart.vue"
import BarChart from "./components/BarChart.vue"
import {
  formatBytes,
  formatClock,
  formatCount,
  formatDuration,
  formatEdge,
  formatInt,
  formatMs,
  formatRate,
  type Bar,
  type Config,
  type Line,
  type NamedConfig,
  type RequestRecord,
  type Snapshot,
} from "./types"

const METHODS = ["GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"]

// 曲线最多留这么多点，超了就隔点抽稀。长压测下不能让数组无限长。
const MAX_POINTS = 2000

// 下拉列表里内置默认配置用的值。开头塞个空字符，保证和用户起的名字撞不上。
const BUILTIN_DEFAULT = "__builtin_default__"

const cfg = ref<Config>({
  url: "https://httpbin.org/",
  method: "GET",
  concurrency: 3,
  duration: 3,
  requests: 0,
  timeout: 3000,
  headers: {},
  body: "",
  host: "",
  noCompression: false,
  noKeepAlive: false,
  skipVerify: false,
  allowRedirects: false,
  http2: true,
  clientCert: "",
  clientKey: "",
  caCert: "",
  recordDetail: false,
})

const headerText = ref("")
const configName = ref("")
const selectedName = ref("")
const savedConfigs = ref<NamedConfig[]>([])
/** 后端给的默认参数。下拉列表里那个「默认配置」项用的就是它。 */
const defaults = ref<Config | null>(null)
const notice = ref("")
const snap = ref<Snapshot | null>(null)
/** 本轮实际用的参数。结果区按它显示 —— 压测中改表单不该影响已经跑出来的结果。 */
const activeCfg = ref<Config | null>(null)
/** 本轮压测开始的墙钟时间，图表用它把「第几秒」换算成真实时刻。 */
const benchStartMs = ref(0)
const records = ref<RequestRecord[]>([])
const lastSeq = ref(0)
const showRecords = ref(true)
const openedRecord = ref<RequestRecord | null>(null)
const timeline = ref<{ t: number; qps: number; p50: number; p99: number }[]>([])
const running = ref(false)
const message = ref("")
const showAdvanced = ref(false)

let offTick: (() => void) | undefined
let offDone: (() => void) | undefined

onMounted(async () => {
  // 默认值以后端为准，免得默认参数在两边各维护一份
  try {
    const d = (await DefaultConfig()) as unknown as Config
    defaults.value = d
    cfg.value = { ...cfg.value, ...d }
    urlInput.value = stripScheme(cfg.value.url)
  } catch (e) {
    message.value = errText(e)
  }

  try {
    const s = await GetSnapshot()
    if (s && s.requests > 0) snap.value = s as unknown as Snapshot
  } catch {
    // 拿不到就没有历史结果，不影响使用
  }

  await loadConfigs()

  offTick = EventsOn("bench:tick", (s: Snapshot) => {
    snap.value = s
    running.value = true
    pushPoint(s)
    void refreshRecords()
  })
  offDone = EventsOn("bench:done", (s: Snapshot) => {
    snap.value = s
    running.value = false
    void refreshRecords()
  })
})

onBeforeUnmount(() => {
  offTick?.()
  offDone?.()
})

function pushPoint(s: Snapshot) {
  timeline.value.push({ t: s.elapsed, qps: s.qps, p50: s.p50Ms, p99: s.p99Ms })
  // 抽稀而不是丢头部，这样长压测还能看到完整的起落趋势
  if (timeline.value.length > MAX_POINTS) {
    timeline.value = timeline.value.filter((_, i) => i % 2 === 0)
  }
}

async function start() {
  message.value = ""
  notice.value = ""
  // 先占住按钮。这一轮要是在 IPC 往返期间就跑完了，bench:done 会先到并把
  // running 置回 false —— 等 await 回来再置 true 的话，界面就永远卡在
  // 「运行中」、「开始压测」一直禁用，只能重启应用。
  running.value = true

  const c = currentConfig()
  try {
    // StartBench 返回的是上一轮的最终快照，别拿它当本轮数据 ——
    // 第一个 tick 500ms 后就到，指标卡不会空太久
    await StartBench(c as unknown as main.Config)
  } catch (e) {
    // 没跑起来（配置没过校验），界面上的上一轮结果原样留着
    running.value = false
    message.value = errText(e)
    return
  }

  // 后端确实接过去了，这才翻篇。放在 await 之后是为了启动失败时不清掉
  // 上一轮的结果。第一个 tick（500ms）远晚于 IPC 往返，不会误删新数据。
  activeCfg.value = c
  timeline.value = []
  snap.value = null
  benchStartMs.value = Date.now()
  records.value = []
  lastSeq.value = 0
  openedRecord.value = null
  // 翻篇：上一轮还在飞的明细请求回来时会因为对不上号被丢掉
  benchRound++
}

async function stop() {
  try {
    await StopBench()
  } catch (e) {
    message.value = errText(e)
  }
}

/** 地址栏当前的协议，给左边那个下拉用。认不出来就按 https 算。 */
const currentScheme = computed(() =>
  cfg.value.url.trim().toLowerCase().startsWith("http://") ? "http://" : "https://",
)

/**
 * 地址框里显示的文字（不含协议），协议由左边下拉管。
 *
 * 它是独立的 ref，不是 cfg.url 的 computed —— 早先做成 computed 双向转换，
 * 手动敲到 "https://" 那一刻 getter 会把前缀剥掉算出空串，输入框当场闪空。
 * 现在框里的内容只由用户输入驱动，外部改 cfg.url 时才反向同步。
 */
const urlInput = ref("")

/** 从完整地址里剥掉协议前缀。 */
function stripScheme(u: string): string {
  return u.trim().replace(/^https?:\/\//i, "")
}

/** 用户改地址框：摘出协议同步给下拉，剩下的写进 cfg.url。 */
function onUrlInput(e: Event) {
  const raw = (e.target as HTMLInputElement).value
  urlInput.value = raw
  const trimmed = raw.trim()
  const matched = trimmed.match(/^(https?):\/\//i)
  cfg.value.url = matched
    ? matched[1].toLowerCase() + "://" + trimmed.slice(matched[0].length)
    : currentScheme.value + trimmed
}

/** 换协议：只替换前缀，后面的部分原样留着。 */
function setScheme(scheme: string) {
  cfg.value.url = scheme + stripScheme(cfg.value.url)
  // 框里可能还留着完整地址（从别处整段粘进来的），跟着一起换，
  // 否则框里写的和目标实际用的对不上
  urlInput.value = stripScheme(cfg.value.url)
}

function onSchemeChange(e: Event) {
  setScheme((e.target as HTMLSelectElement).value)
}

/** 请求头按「一行一个 Key: Value」解析，比在界面上摆一堆键值对输入框好用。 */
function parseHeaders(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const raw of text.split("\n")) {
    const line = raw.trim()
    if (!line) continue
    const i = line.indexOf(":")
    if (i <= 0) continue
    out[line.slice(0, i).trim()] = line.slice(i + 1).trim()
  }
  return out
}

function errText(e: unknown): string {
  if (typeof e === "string") return e
  if (e instanceof Error) return e.message
  return String(e)
}

/** 请求头 map 和「每行一个 Key: Value」的文本互转。加载配置和详情面板都用它。 */
function headerLines(h: Record<string, string> | undefined): string {
  return Object.entries(h ?? {})
    .map(([k, v]) => `${k}: ${v}`)
    .join("\n")
}

/** 当前表单对应的完整配置，请求头已经在里面解析好了。 */
function currentConfig(): Config {
  const c = cfg.value
  return {
    ...c,
    // 数字框被清空时 v-model.number 写进来的其实是空字符串，直接发给后端
    // 会被 JSON 反序列化拒掉，界面只报一串英文。这里兜回数字。
    concurrency: numOr(c.concurrency, 3),
    duration: numOr(c.duration, 3),
    requests: numOr(c.requests, 0),
    timeout: numOr(c.timeout, 3000),
    headers: parseHeaders(headerText.value),
  }
}

/** 数字框可能是空字符串，换算成数字；换不出来就给默认值。 */
function numOr(v: unknown, fallback: number): number {
  const n = typeof v === "number" ? v : Number(v)
  if (!Number.isFinite(n)) return fallback
  // 后端这几个字段是 Go 的 int：小数会被反序列化拒掉，超出安全整数范围的
  // 值 JSON 会写成科学计数法（1e+21），同样解不进去
  const t = Math.trunc(n)
  return Number.isSafeInteger(t) ? t : fallback
}

/** 把一份配置铺到表单上。 */
function applyConfig(c: Config) {
  cfg.value = { ...cfg.value, ...c }
  headerText.value = headerLines(c.headers)
  urlInput.value = stripScheme(c.url ?? "")
}

async function loadConfigs() {
  try {
    savedConfigs.value = (await ListConfigs()) as unknown as NamedConfig[]
  } catch (e) {
    message.value = errText(e)
  }
}

async function saveConfig() {
  message.value = ""
  notice.value = ""
  const name = configName.value.trim()
  try {
    savedConfigs.value = (await SaveConfig(
      name,
      currentConfig() as unknown as main.Config,
    )) as unknown as NamedConfig[]
    selectedName.value = name
    notice.value = `已保存「${name}」`
  } catch (e) {
    message.value = errText(e)
  }
}

async function removeConfig() {
  message.value = ""
  notice.value = ""

  // 内置的默认项不是用户存的，不给删
  if (selectedName.value === BUILTIN_DEFAULT && !configName.value.trim()) {
    message.value = "「默认配置」是内置的，无法删除。想留一份自己的就改完点保存"
    return
  }

  const name = configName.value.trim() || selectedName.value
  if (!name) {
    message.value = "先选一个要删的配置"
    return
  }
  try {
    savedConfigs.value = (await DeleteConfig(name)) as unknown as NamedConfig[]
    if (selectedName.value === name) selectedName.value = ""
    configName.value = ""
    notice.value = `已删除「${name}」`
  } catch (e) {
    message.value = errText(e)
  }
}

/** 拉取中标记，避免上一次 IPC 还没回来就又发一次。 */
let recordsFetching = false
/** 有拉取在飞的时候又来了新请求 —— 等它回来立刻补一次。 */
let recordsPending = false
/** 压测轮次号。迟到的响应回来时对不上号就丢掉。 */
let benchRound = 0

/**
 * 按 seq 拉取新的请求明细。后端只留最近 1000 条，这里也维持在同样规模。
 * 压测中跟着 tick 走；面板收起时不拉，省掉不必要的 IPC。
 */
async function refreshRecords() {
  if (!showRecords.value) return
  if (recordsFetching) {
    // 在飞的那次可能拿的是旧数据（bench:done 那一次尤其如此，
    // 而 done 之后再没有别的时机了），记下来让它回来时补一次
    recordsPending = true
    return
  }
  recordsFetching = true
  const round = benchRound
  try {
    const got = (await RecentRequests(lastSeq.value)) as unknown as RequestRecord[]
    // 这一轮已经翻篇了，迟到的结果扔掉 —— 否则会把上一轮的 seq 写回 lastSeq，
    // 而新一轮是新建的缓冲、序号从 1 重新数，明细面板会整轮空白
    if (round !== benchRound) return
    if (!got.length) return
    lastSeq.value = got[got.length - 1].seq
    records.value = [...records.value, ...got].slice(-1000)
  } catch {
    // 明细拉不到不影响压测本身，静默跳过
  } finally {
    recordsFetching = false
    if (recordsPending) {
      recordsPending = false
      void refreshRecords()
    }
  }
}

/** 展开面板时补拉一次，免得收起那段时间的记录漏掉。 */
function toggleRecords() {
  showRecords.value = !showRecords.value
  if (showRecords.value) void refreshRecords()
}

/** 最新一条排在最上面，像日志那样。 */
const recentRecords = computed(() => [...records.value].reverse())

/**
 * 清空明细列表。
 * 只清前端这份，而且刻意不动 lastSeq —— 留着它，后续拉取就只会取新产生的
 * 记录。要是把它归零，下一轮就会把后端缓冲里刚清掉的那些又拉回来。
 */
function clearRecords() {
  records.value = []
  openedRecord.value = null
  message.value = ""
  notice.value = "已清空明细"
}

/**
 * 点一行展开或收起它的详情。
 *
 * 明细节里的文字是可以拖选复制的，而拖选之后松手同样会触发 click ——
 * 不挡一下的话，想复制个时间戳就会把详情弹出来。
 */
function toggleRow(r: RequestRecord) {
  if (window.getSelection()?.toString()) return
  openedRecord.value = openedRecord.value?.seq === r.seq ? null : r
}

/** 从下拉里选一项铺到表单上。 */
function pickConfig() {
  message.value = ""
  notice.value = ""

  if (selectedName.value === BUILTIN_DEFAULT) {
    if (defaults.value) applyConfig(defaults.value)
    configName.value = ""
    notice.value = "已恢复默认配置"
    return
  }

  const found = savedConfigs.value.find((c) => c.name === selectedName.value)
  if (!found) return
  applyConfig(found.config)
  configName.value = found.name
}

async function exportConfig() {
  message.value = ""
  notice.value = ""
  try {
    const path = await ExportConfig(currentConfig() as unknown as main.Config)
    if (path) notice.value = `已导出到 ${path}`
  } catch (e) {
    message.value = errText(e)
  }
}

async function importConfig() {
  message.value = ""
  notice.value = ""
  try {
    const c = (await ImportConfig()) as unknown as Config
    if (!c?.url) return // 用户取消了对话框
    applyConfig(c)
    notice.value = "已从文件载入配置"
  } catch (e) {
    message.value = errText(e)
  }
}

const qpsLines = computed<Line[]>(() => [
  {
    name: "QPS",
    color: "#4ade80",
    points: timeline.value.map((p) => ({ x: p.t, y: p.qps })),
  },
])

const latencyLines = computed<Line[]>(() => [
  {
    name: "P50",
    color: "#60a5fa",
    points: timeline.value.map((p) => ({ x: p.t, y: p.p50 })),
  },
  {
    name: "P99",
    color: "#f472b6",
    points: timeline.value.map((p) => ({ x: p.t, y: p.p99 })),
  },
])

const latencyBars = computed<Bar[]>(() => {
  const bins = snap.value?.latencyBins ?? []
  const total = snap.value?.requests ?? 0
  let cum = 0
  return bins.map((b) => {
    cum += b.count
    return {
      label: formatMs(b.lowerMs),
      value: b.count,
      tip: [
        `${formatEdge(b.lowerMs)} ~ ${formatEdge(b.upperMs)}`,
        `${formatCount(b.count)} 次 · ${pctOf(b.count, total)}`,
        `≤ 此区间累计 ${pctOf(cum, total)}`,
      ],
    }
  })
})

/** 占总数多少。 */
function pctOf(n: number, total: number): string {
  return total > 0 ? ((n / total) * 100).toFixed(1) + "%" : "—"
}

const statusList = computed(() =>
  Object.entries(snap.value?.statusCodes ?? {})
    .map(([code, n]) => ({ code, n }))
    .sort((a, b) => a.code.localeCompare(b.code)),
)

const errorList = computed(() =>
  Object.entries(snap.value?.errorMap ?? {})
    .map(([text, n]) => ({ text, n }))
    .sort((a, b) => b.n - a.n),
)

const errorRate = computed(() => {
  const s = snap.value
  if (!s) return 0
  const total = s.requests + s.errors
  return total === 0 ? 0 : (s.errors / total) * 100
})

/** 每秒收到的响应字节数，以及平均每个响应多大。 */
const throughput = computed(() => formatRate(snap.value?.bytes ?? 0, snap.value?.elapsed ?? 0))

const avgRespSize = computed(() => {
  const s = snap.value
  if (!s || s.requests <= 0) return "—"
  return formatBytes(s.bytes / s.requests)
})

/** 实际并发 = QPS × 平均延迟（Little's Law），用来核对设定的并发有没有跑满。 */
const realConcurrency = computed(() => {
  const s = snap.value
  if (!s) return 0
  return (s.avgQps * s.avgMs) / 1000
})

/** 这一轮结束的墙钟时刻，等于开始时刻加上总耗时。
    跑的过程中还不知道结果，所以只有结束后才有值（0 表示没有）。 */
const benchEndMs = computed(() => {
  const s = snap.value
  if (!benchStartMs.value || !s || running.value) return 0
  return benchStartMs.value + Math.round(s.elapsed * 1000)
})

/** 请求数模式的进度。时长模式没有总数这个概念，这张卡不显示。 */
const progressPct = computed(() => {
  const total = activeCfg.value?.requests ?? 0
  const done = snap.value?.requests ?? 0
  if (total <= 0) return 0
  return Math.min(100, (done / total) * 100)
})

/** 非 2xx 的响应，按 3xx / 4xx / 5xx 分开数。
    它和「传输失败」是两回事：这里是服务端答了但答得不对，那里是压根没答上。
    3xx 也算 —— 卡片叫「非 2xx」，默认又不跟随重定向，压一个会跳转的目标时
    状态码面板列的是 3xx，这张卡要是只数 4xx/5xx 就和它对不上了。 */
const badResponses = computed(() => {
  let c3 = 0
  let c4 = 0
  let c5 = 0
  for (const [code, n] of Object.entries(snap.value?.statusCodes ?? {})) {
    const c = Number(code) // 「其它」这类非数字键会变成 NaN，下面的比较自然为假
    if (c >= 500 && c < 600) c5 += n
    else if (c >= 400 && c < 500) c4 += n
    else if (c >= 300 && c < 400) c3 += n
  }
  return { c3, c4, c5, total: c3 + c4 + c5 }
})

const badRate = computed(() => {
  const done = snap.value?.requests ?? 0
  return done > 0 ? (badResponses.value.total / done) * 100 : 0
})

/** 非 2xx 的明细，只列出现过的那几段 —— 三段都写上卡片放不下。 */
const nonOkBreakdown = computed(() => {
  const b = badResponses.value
  const parts: string[] = []
  if (b.c5) parts.push(`5xx ${formatInt(b.c5)}`)
  if (b.c4) parts.push(`4xx ${formatInt(b.c4)}`)
  if (b.c3) parts.push(`3xx ${formatInt(b.c3)}`)
  return parts.join(" · ")
})

/** 抖动的相对值：标准差 ÷ 平均值。光看标准差没法和别的接口比 ——
    平均 20ms 抖 5ms 和平均 200ms 抖 5ms，稳定程度差得远。 */
const latencyCv = computed(() => {
  const s = snap.value
  if (!s || s.avgMs <= 0) return 0
  return (s.stdDevMs / s.avgMs) * 100
})

const n1 = (v?: number) => (v ?? 0).toFixed(1)

/** QPS 的悬停提示保留一位小数。用命名函数，免得模板里每次渲染都建个新函数。 */
const fmtQps = (v: number) => v.toFixed(1)

/** 状态码按首位着色：2xx 绿、3xx 蓝、4xx 橙、5xx 红。
    认不出的（落在 100~599 之外的，后端记作「其它」）给中性色 ——
    否则拼出来的类名没有对应样式，进度条会落到默认的绿色上。 */
function statusClass(code: string): string {
  const c = code.charAt(0)
  return c >= "1" && c <= "5" ? "s" + c : "sx"
}
</script>

<template>
  <div class="app">
    <header class="topbar">
      <div class="brand">
        <span class="dot" :class="{ live: running }"></span>
        <h1>go-wrk-desktop</h1>
      </div>
      <div class="status">
        <template v-if="running">运行中 · {{ n1(snap?.elapsed) }} 秒</template>
        <template v-else-if="snap">已完成 · {{ n1(snap?.elapsed) }} 秒</template>
        <template v-else>就绪</template>
      </div>
    </header>

    <div class="body">
      <!-- 左：配置 -->
      <aside class="panel config">
        <div class="config-store">
          <div class="row">
            <input v-model="configName" placeholder="配置名，比如「订单接口」" spellcheck="false" />
            <button class="mini" @click="saveConfig">保存</button>
            <button class="mini" @click="removeConfig">删除</button>
          </div>
          <div class="row">
            <select v-model="selectedName" @change="pickConfig">
              <option value="">选择已保存的配置</option>
              <option :value="BUILTIN_DEFAULT">默认配置（httpbin.org）</option>
              <option v-for="c in savedConfigs" :key="c.name" :value="c.name">{{ c.name }}</option>
            </select>
            <button class="mini" @click="exportConfig">导出</button>
            <button class="mini" @click="importConfig">打开</button>
          </div>
        </div>

        <div class="field">
          <div class="field-head">
            <span>目标地址<em class="req">*</em></span>
            <select :value="currentScheme" @change="onSchemeChange">
              <option value="https://">https://</option>
              <option value="http://">http://</option>
            </select>
          </div>
          <input
            :value="urlInput"
            placeholder="httpbin.org/api"
            spellcheck="false"
            @input="onUrlInput"
            @keyup.enter="!running && start()"
          />
        </div>

        <div class="row">
          <label class="field">
            <span>方法</span>
            <select v-model="cfg.method">
              <option v-for="m in METHODS" :key="m" :value="m">{{ m }}</option>
            </select>
          </label>
          <label class="field">
            <span>超时（毫秒）</span>
            <input v-model.number="cfg.timeout" type="number" min="1" />
          </label>
        </div>

        <div class="row">
          <label class="field">
            <span>并发数</span>
            <input v-model.number="cfg.concurrency" type="number" min="1" max="10000" />
          </label>
          <label class="field">
            <span>时长（秒）</span>
            <input v-model.number="cfg.duration" type="number" min="1" :disabled="cfg.requests > 0" />
          </label>
        </div>

        <label class="field">
          <span>请求数<em>填了就按请求数执行，忽略时长</em></span>
          <input v-model.number="cfg.requests" type="number" min="0" placeholder="0 = 按时长执行" />
        </label>

        <label class="field">
          <span>请求头<em>每行一个 Key: Value</em></span>
          <textarea
            v-model="headerText"
            rows="3"
            spellcheck="false"
            placeholder="Content-Type: application/json"
          ></textarea>
        </label>

        <label class="field">
          <span>请求体</span>
          <textarea v-model="cfg.body" rows="3" spellcheck="false" placeholder="POST / PUT 时发送的内容"></textarea>
        </label>

        <button class="toggle" @click="showAdvanced = !showAdvanced">
          {{ showAdvanced ? "▾" : "▸" }} 高级选项
        </button>

        <div v-if="showAdvanced" class="advanced">
          <label class="check"><input v-model="cfg.http2" type="checkbox" /> 启用 HTTP/2</label>
          <label class="check"><input v-model="cfg.allowRedirects" type="checkbox" /> 跟随重定向</label>
          <label class="check"><input v-model="cfg.noCompression" type="checkbox" /> 禁用压缩（不发 Accept-Encoding）</label>
          <label class="check"><input v-model="cfg.noKeepAlive" type="checkbox" /> 禁用 Keep-Alive（每请求新建连接）</label>
          <label class="check"><input v-model="cfg.skipVerify" type="checkbox" /> 跳过证书校验</label>

          <label class="field">
            <span>Host 覆盖<em>留空则用地址里的主机名</em></span>
            <input v-model="cfg.host" spellcheck="false" placeholder="example.com" />
          </label>
          <label class="field">
            <span>客户端证书 / 私钥<em>双向 TLS 用，两个都要填</em></span>
            <input v-model="cfg.clientCert" spellcheck="false" placeholder="client.crt" />
            <input v-model="cfg.clientKey" spellcheck="false" placeholder="client.key" class="mt" />
          </label>
          <label class="field">
            <span>CA 证书<em>服务端用自签证书时填它</em></span>
            <input v-model="cfg.caCert" spellcheck="false" placeholder="ca.crt" />
          </label>
        </div>

        <div class="actions">
          <button class="primary" :disabled="running" @click="start">
            {{ running ? "压测中…" : "开始压测" }}
          </button>
          <button class="ghost" :disabled="!running" @click="stop">停止</button>
        </div>
        <p v-if="message" class="message">{{ message }}</p>
        <p v-else-if="notice" class="notice">{{ notice }}</p>
      </aside>

      <!-- 右：结果 -->
      <section class="panel results">
        <div class="cards">
          <div class="card" title="最近 500 毫秒的瞬时 QPS。下面一行是全程平均。">
            <label>QPS</label>
            <b class="accent-green">{{ n1(snap?.qps) }}</b>
            <em>平均 {{ n1(snap?.avgQps) }}</em>
          </div>
          <div class="card" title="每秒收到的响应字节数（响应头算在内）。下面一行是平均每个响应的大小。">
            <label>吞吐量</label>
            <b>{{ throughput }}</b>
            <em>单个 {{ avgRespSize }}</em>
          </div>
          <div class="card" title="所有响应的平均耗时。下面一行是 P50 —— 一半的请求比它快。">
            <label>平均延迟</label>
            <b>{{ formatMs(snap?.avgMs ?? 0) }}</b>
            <em>P50 {{ formatMs(snap?.p50Ms ?? 0) }}</em>
          </div>
          <div class="card" title="延迟的标准差：越大说明请求快慢越不匀。下面一行是它占平均延迟的比例，方便跨接口比较。">
            <label>延迟抖动</label>
            <b>{{ formatMs(snap?.stdDevMs ?? 0) }}</b>
            <em>波动 {{ n1(latencyCv) }}%</em>
          </div>
          <div class="card" title="P99：99% 的请求都比它快，用来衡量长尾。下面一行是 P90。">
            <label>P99 延迟</label>
            <b class="accent-pink">{{ formatMs(snap?.p99Ms ?? 0) }}</b>
            <em>P90 {{ formatMs(snap?.p90Ms ?? 0) }}</em>
          </div>
          <div class="card" title="本轮最快的一次请求延迟。">
            <label>最快</label>
            <b>{{ formatMs(snap?.minMs ?? 0) }}</b>
            <em>单次最短</em>
          </div>
          <div class="card" title="本轮最慢的一次请求延迟。它比 P99 大得越多，说明有个别请求被拖得越久。">
            <label>最慢</label>
            <b>{{ formatMs(snap?.maxMs ?? 0) }}</b>
            <em>单次最长</em>
          </div>
          <div class="card" title="按 Little's Law 反推的平均在飞请求数（QPS × 平均延迟）。下面一行是设定值 —— 比它低得多，说明连接没占满。">
            <label>实际并发数</label>
            <b>{{ n1(realConcurrency) }}</b>
            <em v-if="activeCfg">设定 {{ activeCfg.concurrency }}</em>
            <em v-else>—</em>
          </div>
          <div class="card" title="完成的请求总数，含 4xx/5xx。下面一行是收到的响应总字节数。">
            <label>总请求数</label>
            <b>{{ formatCount(snap?.requests ?? 0) }}</b>
            <em>{{ formatBytes(snap?.bytes ?? 0) }}</em>
          </div>
          <div v-if="(activeCfg?.requests ?? 0) > 0" class="card" title="请求数模式的进度，发满设定数量就停。下面一行是已完成 / 总数。">
            <label>请求进度</label>
            <b>{{ n1(progressPct) }}%</b>
            <em>{{ formatInt(snap?.requests ?? 0) }} / {{ formatInt(activeCfg?.requests ?? 0) }}</em>
          </div>
          <div class="card" title="服务端答了但状态码不是 2xx 的响应。它和「传输失败」是两回事：这里至少还收到了回复。">
            <label>非 2xx 响应</label>
            <b :class="{ 'accent-red': badResponses.total > 0 }">{{ n1(badRate) }}%</b>
            <em v-if="badResponses.total > 0">{{ nonOkBreakdown }}</em>
            <em v-else>全部 2xx</em>
          </div>
          <div class="card" title="传输层失败次数：连不上、超时、读中断。4xx/5xx 不算在这里，它们计入状态码分布。">
            <label>传输失败</label>
            <b :class="{ 'accent-red': (snap?.errors ?? 0) > 0 }">{{ formatInt(snap?.errors ?? 0) }}</b>
            <em>{{ n1(errorRate) }}%</em>
          </div>
          <div class="card wide" title="这一轮从开始到结束的实际时长。结束时刻要等跑完才知道。">
            <label>总耗时</label>
            <b>{{ formatDuration(snap?.elapsed ?? 0) }}</b>
            <em v-if="benchStartMs">开始于 {{ formatClock(benchStartMs) }}</em>
            <em v-if="benchEndMs">结束于 {{ formatClock(benchEndMs) }}</em>
          </div>
        </div>

        <div class="chart-block">
          <h3>QPS（次/秒）</h3>
          <LineChart
            :lines="qpsLines"
            :height="170"
            :start-ms="benchStartMs"
            :format="fmtQps"
          />
        </div>

        <div class="chart-block">
          <h3>延迟（毫秒）</h3>
          <LineChart
            :lines="latencyLines"
            :height="170"
            :start-ms="benchStartMs"
            :format="formatMs"
          />
        </div>

        <div class="split">
          <div class="chart-block">
            <h3>状态码</h3>
            <div v-if="statusList.length" class="list">
              <div v-for="s in statusList" :key="s.code" class="list-row">
                <span class="badge" :class="statusClass(s.code)">{{ s.code }}</span>
                <span class="bar-track">
                  <span
                    class="bar-fill"
                    :class="statusClass(s.code)"
                    :style="{ width: ((s.n / (snap?.requests || 1)) * 100).toFixed(1) + '%' }"
                  ></span>
                </span>
                <span class="num">{{ formatCount(s.n) }}</span>
              </div>
            </div>
            <p v-else class="hint">无响应</p>
          </div>

          <div class="chart-block">
            <h3 title="横轴按对数分桶：越靠右延迟越大，同样的横向距离代表同样的倍数">延迟分布</h3>
            <BarChart :bars="latencyBars" :height="150" />
          </div>
        </div>

        <div v-if="errorList.length" class="chart-block">
          <h3>传输失败分布</h3>
          <div class="list">
            <div v-for="e in errorList" :key="e.text" class="list-row error">
              <span class="msg" :title="e.text">{{ e.text }}</span>
              <span class="num">{{ formatInt(e.n) }}</span>
            </div>
          </div>
        </div>

        <div class="chart-block">
          <div class="block-head">
            <h3>请求明细</h3>
            <div class="head-right">
              <label
                class="check inline"
                title="连请求头和响应内容一起记下来，点开明细就能看。压测并发高时会多占一点内存。"
              >
                <input v-model="cfg.recordDetail" type="checkbox" :disabled="running" />
                记录完整内容
              </label>
              <button class="mini" :disabled="!records.length" @click="clearRecords">清空</button>
              <button class="mini" @click="toggleRecords">
                {{ showRecords ? "收起" : "展开" }}
              </button>
            </div>
          </div>

          <template v-if="showRecords">
            <p class="hint">只留最近 1000 条，500 毫秒刷新一次。选中该行查看详情。</p>

            <div v-if="recentRecords.length" class="rec-list">
              <div class="rec rec-head">
                <span class="seq">序号</span>
                <span class="col-time">开始时间</span>
                <span class="col-time">结束时间</span>
                <span class="code">状态</span>
                <span class="lat">延迟</span>
                <span class="sz">大小</span>
                <span class="tail">地址 / 错误</span>
              </div>
              <div
                v-for="r in recentRecords"
                :key="r.seq"
                class="rec"
                :class="{ on: openedRecord?.seq === r.seq }"
                @click="toggleRow(r)"
              >
                <span class="seq">{{ r.seq }}</span>
                <span class="col-time">{{ formatClock(r.startMs) }}</span>
                <span class="col-time">{{ formatClock(r.startMs + Math.round(r.latencyMs)) }}</span>
                <span class="code" :class="statusClass(String(r.status))">{{ r.status || "—" }}</span>
                <span class="lat">{{ Math.round(r.latencyMs) }}ms</span>
                <span class="sz">{{ formatBytes(r.size) }}</span>
                <span class="tail" :class="{ bad: r.error }" :title="r.error || r.url">
                  {{ r.error || r.url }}
                </span>
              </div>
            </div>
            <p v-else class="hint">无请求</p>

            <div v-if="openedRecord" class="rec-detail">
              <div class="pane">
                <h4>请求</h4>
                <p class="stamp">开始 {{ formatClock(openedRecord.startMs) }}</p>
                <pre>{{ openedRecord.method }} {{ openedRecord.url }}</pre>
                <template v-if="openedRecord.reqHeaders">
                  <pre>{{ headerLines(openedRecord.reqHeaders) }}</pre>
                  <pre v-if="openedRecord.reqBody">{{ openedRecord.reqBody }}</pre>
                </template>
                <p v-else class="hint">未记录内容。勾选「记录完整内容」后重新压测。</p>
              </div>
              <div class="pane">
                <h4>响应</h4>
                <pre v-if="openedRecord.error" class="bad">{{ openedRecord.error }}</pre>
                <template v-else>
                  <p class="stamp">
                    结束 {{ formatClock(openedRecord.startMs + Math.round(openedRecord.latencyMs)) }}
                  </p>
                  <pre>{{ openedRecord.status }} · {{ openedRecord.latencyMs.toFixed(1) }}ms · {{ formatBytes(openedRecord.size) }}</pre>
                  <template v-if="openedRecord.respHeaders">
                    <pre>{{ headerLines(openedRecord.respHeaders) }}</pre>
                    <pre v-if="openedRecord.respBody">{{ openedRecord.respBody }}</pre>
                    <p v-if="openedRecord.bodyCut" class="hint">内容太长，只留了前面一段。</p>
                  </template>
                  <p v-else class="hint">未记录内容。勾选「记录完整内容」后重新压测。</p>
                </template>
              </div>
            </div>
          </template>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped src="./app.css"></style>
