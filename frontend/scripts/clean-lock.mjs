// 把 package-lock.json 里的 resolved 地址从内网 npm 源改写成公网地址。
//
// 本机的 npm 源指向公司内网 Nexus，每次 npm install 都会把内网地址写回
// lock 文件的 resolved 字段。提交前跑一下：
//
//   npm run clean-lock
//
// 要清理的地址是从 npm 配置里读的，不写死在代码里 —— 写死等于换个地方泄漏。
// 只改 URL 前缀，integrity 校验值不受影响：包的内容没变，只是换个下载地址。
import { execSync } from "node:child_process"
import { readFileSync, writeFileSync } from "node:fs"

const FILE = new URL("../package-lock.json", import.meta.url)
const PUBLIC = "https://registry.npmjs.org/"

const registry = execSync("npm config get registry", { encoding: "utf8" }).trim()

if (!registry.startsWith("http")) {
  console.error(`npm 源配置读出来不像个地址：${registry}`)
  process.exit(1)
}

if (registry === PUBLIC) {
  console.log("npm 源已经是公网地址，无需清理")
  process.exit(0)
}

const before = readFileSync(FILE, "utf8")
const parts = before.split(registry)

if (parts.length === 1) {
  console.log("lock 文件里没有内网地址，无需清理")
} else {
  writeFileSync(FILE, parts.join(PUBLIC))
  console.log(`已替换 ${parts.length - 1} 处内网地址 → ${PUBLIC}`)
}
