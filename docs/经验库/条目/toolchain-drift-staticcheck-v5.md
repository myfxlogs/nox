---
schema_version: 2
id: toolchain-drift-staticcheck-v5
kind: pitfall
risk: high
status: verified
keywords: [staticcheck, export data version 5, GOTOOLCHAIN, go1.27, pre-commit 门禁, 误拦, 工具链漂移]
scope: [开发机环境, go 工具链, git hooks]
summary: 本机 go 升到比 go.mod 钉版更新的版本后，GOTOOLCHAIN=auto 取 max(本机,go.mod)=新版 → 包产出新版 export data，staticcheck 旧版解不动 → pre-commit staticcheck 基线闸对一切 commit 报 "export data version N is greater than maximum supported" 并误当新违规拒收；`GOTOOLCHAIN=<go.mod版> git commit` 即恢复，禁 --no-verify。
first_seen: 2026-10-09
last_verified: 2026-10-09
skills: [KB维护]
evidence_refs:
  - type: decision
    ref: D-153
  - type: commit
    ref: 4fd51f105
  - type: server_fact
    source: postgres
    query: "本机实拍：go version=go1.27.2 vs go.mod go 1.26.5；staticcheck 2026.2.1(0.8.1)=@latest 无更新版"
    observed_at: 2026-10-09T12:30:00Z
---
## 事实/症状
`git commit` 被 pre-commit 拦，输出形如 `internal error in importing "internal/goarch" (cannot decode "internal/goarch", export data version 5 is greater than maximum supported version 4)`——**指向标准库的误导性报错**，被钩子当「新基线违规」拒收，与 diff 内容零相关。
## 根因
`GOTOOLCHAIN=auto` 语义=取 max(本机工具链, go.mod `go` 指令)，**不会向下钉版**。本机 go1.27.2 > go.mod 1.26.5 → 一切编译走 1.27 产 v5 export data；staticcheck 0.8.1 只认到 v4。Docker 构建（golang:1.26-alpine）不受影响——纯开发机面。
## 修法
临时：`GOTOOLCHAIN=go1.26.5 git commit`（钩子继承 env，go list 切钉版产 v4，staticcheck 恢复正常）。根治候选已立 D-153：钩子里按 go.mod 钉 GOTOOLCHAIN / `go env -w` 用户级钉版 / 等 staticcheck 新版。
## 反证条件 / 适用边界
staticcheck 发布支持 v5 export data 的版本后本条退休；本机 go 回退到 ≤go.mod 版也不复现。
## 泛化判断
通用坑：任何「宿主工具链新于仓库钉版 + staticcheck 旧版」组合都会踩——CI 固定镜像不复发，裸机开发席是高发面。
