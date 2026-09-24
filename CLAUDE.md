# CLAUDE.md

给 AI 助手和开发者的项目说明。面向使用者的介绍在 README.md。

## 常用命令

```bash
wails build                    # 构建，产物在 build/bin/
wails dev                      # 开发模式，前端热重载
go test ./...                  # 全部测试
cd frontend && npm run build   # 只构建前端（含 vue-tsc 类型检查）
```

**提交前在 `frontend/` 下跑 `npm run clean-lock`** —— 本机 npm 源指向公司内网，
`npm install` 会把内网地址写进 `package-lock.json`，这个脚本把它换回公网地址。

## 分支与发布

- `dev` —— 日常开发，提交都落在这里
- `main` —— 稳定分支，只用来打标签发版

发版：`git checkout main && git merge dev && git tag v1.0.0 && git push origin main --tags`

标签必须打在 main 上 —— `.github/workflows/release.yml` 会检查，dev 上的标签会被拒。
工作流负责跑测试、`wails build`、把 exe 挂到 Release 上。

## 代码结构

```
engine.go    压测引擎：worker 池、请求循环、配置校验、错误归类
stats.go     延迟直方图和快照结构
records.go   请求明细的环形缓冲
configs.go   配置的保存/加载/导入导出
app.go       绑定给前端的方法；压测进度走 Wails 事件，前端不轮询
main.go      Wails 启动
frontend/src/App.vue                    界面
frontend/src/app.css                    App.vue 的样式
frontend/src/components/LineChart.vue   实时曲线
frontend/src/components/BarChart.vue    延迟分布
frontend/src/types.ts                   类型和格式化
frontend/src/useElementWidth.ts         宽度监听（图表按真实像素画，不用 SVG 缩放，免得字被拉变形）
```

## 引擎的取舍

改引擎前先看这几条，都是有意的：

1. **每个 worker 独占一个 `http.Client`** —— 并发数就是真实连接数，也避开共享连接池的锁竞争。
2. **统计全走原子操作 + 固定桶直方图** —— 主循环随时能读出一份快照，不必等 worker 结束，所以才有实时曲线。直方图按 2 的幂分 32 个子桶，相对误差约 3%，内存固定不到 1000 个计数器。
3. **响应体用 `io.Copy` 丢弃，只数字节** —— 目标返回大响应时不会把内存吃满。开了「记录完整内容」才顺手截留前 4KB。
4. **客户端构造失败只让那个 worker 退出并记一条错误**，不像原版那样 `log.Fatal` 把整个进程带走。
5. **自己按的停止不算目标的错** —— 取消导致的失败不进错误统计，免得停止瞬间错误数暴涨。
6. **`maxTimeoutMs`（10 分钟）和直方图的量程绑在一起** —— `stats.go` 的 `histMaxExp` 要盖得住它（现在到约 2200 秒）。改其中一个记得看另一个，超了的话慢样本会全挤进最后一个桶，百分位就不可分辨了。`TestHistogramCoversMaxTimeout` 会拦下不配套的改动。

## 换应用图标

图丢进 `image/`，跑 `.\scripts\make-icon.ps1`（中文系统下这脚本必须带 UTF-8 BOM）。
生成 `build/appicon.png`（1024×1024）和 `build/windows/icon.ico`（16/32/48/64/128/256），再 `wails build`。

两个坑：Wails 只在 `icon.ico` **不存在**时才从 appicon.png 生成它，已经有一份就不再覆盖 ——
所以换图必须跑脚本，光替换 appicon.png 没用；图最好是正方形，不是的话脚本会补边，
但内容会相应变小。
