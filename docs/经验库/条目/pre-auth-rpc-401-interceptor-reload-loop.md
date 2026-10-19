---
schema_version: 2
id: pre-auth-rpc-401-interceptor-reload-loop
kind: pitfall
risk: high
status: observed
keywords: [登录页刷新循环, 401拦截器, location.reload, useActionBadge, unauthenticated, 预登录RPC]
scope: [web/src 壳层, 全局 RPC 拦截器, 任何带登录门+全局错误重定向的前端]
summary: hooks 先于登录门×401 reload 拦截器=登录页刷新环；双修=登录页不 reload+预登录零发 RPC。
first_seen: 2026-10-19
last_verified: 2026-10-19
skills: []
evidence_refs:
  - type: server_fact
    source: rpc
    query: POST /nox.dashboard.v1.DashboardService/GetActionBadge (no auth) @SG
    observed_at: 2026-10-19T13:20Z
    server_revision: nox-core 2026-10-19T12:4xZ 部署版
    response_hash: ""
  - type: doc
    ref: web/src/api.ts:27
  - type: doc
    ref: web/src/main.tsx useActionBadge
---

## 事实/症状

业主实报（10-19，TOTP 上线当天）：登录页无限刷新，6 位码根本填不完。服务端实拍：无鉴权调 GetActionBadge 回 `{"code":"unauthenticated","message":"missing authorization header"}` http=401——而 main.tsx 的 useActionBadge() 钩子在 App() 登录门 early-return 之前无条件执行（React hooks 规则所致），未登录也发。

## 根因或策略解释

两机制相乘：①hooks 先于登录门执行=预登录期存在 authed RPC 噪音（历史上无害——401 被吞）；②TOTP 票新增拦截器把 401 升级为 clearToken+hash #/login+location.reload()（session 过期自动回登录的禁线实现）——登录页上每次重挂载钩子再发 401 再 reload，成环。单看任一件都不是 bug；组合=登录面整死。此为「全局副作用处理器 × 无条件前置调用」的经典放大模式。

## 处置

双修缺一不可：①api.ts 拦截器加环断路——已在 #/login 只清 token 不 reload（任何来源的 401 都不可能在登录页成环）；②useActionBadge effect 预登录 early-return（掐源头，省无谓 401）。新鉴权面接入清单补一条：登录门之前的 effect 禁调 authed RPC。

## 证据

- check: curl 无鉴权 GetActionBadge @SG
- result: 401 {"code":"unauthenticated",...}（2026-10-19T13:20Z）；修后 vite build 净
- ref: wip/logout-btn 叠加 commit（10-19）

## 反证条件

- check: 修后在登录页挂 Network 面板观察 GetActionBadge 是否发出/页面是否刷新
- falsifies_when: 拦截器修+badge 门双落仍循环（存在第三发起点或 reload 调用点）→ 本条双修结论失效
