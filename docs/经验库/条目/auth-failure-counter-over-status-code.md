---
schema_version: 2
id: auth-failure-counter-over-status-code
kind: pitfall
risk: high
status: verified
keywords: [第三方 auth, JWT refresh, 500 非 401, 状态码不可信, 重登兜底, 连续失败计数]
scope: [collect, ops]
summary: 第三方认证 session 失效可能返回非标状态码（500 而非 401）——重登判断不能依赖状态码语义，须基于连续失败计数兜底
first_seen: 2026-09-04
last_verified: 2026-09-04
skills: []
evidence_refs:
  - type: decision
    ref: D-214（decisions.md）
  - type: commit
    ref: 956bf99
---

## 事实/症状

CMC 采集器 JWT refresh 链：代码按标准语义只在 401 时回退重新登录，其余非 200 返回错误交调度器退避重试。9-3 15:47 UTC 起服务端 session 失效后 platform multirequest 返回 **500**（非 401）→ 永不触发重登 → 32s 退避死循环 attempt 919 次、数据断流 8h+。重启进程（走全新登录链）立即恢复——证明登录凭据本身有效，死的只是 refresh 路径。

## 根因或策略解释

「401=未授权要重登」是 RFC 语义期望，第三方网关/服务端实现未必遵守（session 失效走内部错误路径变 500）。按状态码分支的恢复逻辑在状态码不守约时退化为无限重试同一条死路。**状态码不可信任时，恢复决策要基于自己的可观测信号**（连续失败次数），状态码只作为加速提示（401 仍立即重登）。

## 处置

AuthManager 加 refreshFails 连续失败计数：非 200 非 401 累计 ≥3 → 置零并回退 loginUnlocked（sched 退避 2s/4s/8s 下约 14s 自愈）；成功/重登成功清零；401 立即重登保留。阈值 3 平衡：单次瞬时 5xx 不误杀活 session，持续失败快速自愈。

## 证据

- 生产实录：`collect:cmc_prices poll failed (attempt 919): refreshJWT: status 500`（8h+ 持续）；重启后 facts 恢复新鲜（39s/3m）
- 代码：internal/collect/cmc/auth.go refreshJWT（401 分支/计数分支/清零三路）
- 测试：auth_relogin_test.go 3 测试（连败 3 次重登/瞬时失败不重登/成功清零防跨轮累计）

## 反证条件

若服务端在凭据错误（密码改了）时也返回 500，本方案退化为每 ~2min 一次完整重登链尝试（4 次退避后触发）——非风暴但日志可见，属可接受诊断信号而非缺陷。

## 适用边界

长生命周期第三方 session/JWT 客户端。短命批处理任务（失败即整体退出）不需要。
