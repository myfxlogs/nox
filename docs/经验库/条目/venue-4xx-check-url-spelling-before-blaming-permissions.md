---
schema_version: 2
id: venue-4xx-check-url-spelling-before-blaming-permissions
kind: pitfall
risk: high
status: verified
keywords: [403, 404, 权限误诊, URL 拼接, purchase-redempt, api/v4 双前缀, OKX, Gate, 网关层拒绝, earn rail]
scope: [dciexec, earn_rail, venue 集成, 签名探针, 权限诊断]
summary: venue 返回 403/404 先验 URL 拼写再议权限——gate 双 /api/v4 前缀回 HTML 404、okx purchase-redemption 错拼（真路径=截断形 -redempt）回 JSON 403；两案都被误诊为权限/账户族问题浪费数拍，签名探针空参请求（过权限层出业务参数错=路径真）是最快定案器。
first_seen: 2026-10-14
last_verified: 2026-10-14
skills: [生产取证]
evidence_refs:
  - type: commit
    ref: a24df0e2
  - type: commit
    ref: 8b39b908
  - type: server_fact
    ref: "SG 探针：POST purchase-redemption=403；POST purchase-redempt 空参=400 50014 参数错、带参 amt:10=200 code:0 真实成交"
---

# venue 4xx 先查路径拼写，再议权限——两次误诊同一病灶

## 症状

两个 venue 连续踩同型坑，且各自先被误诊一轮：

| venue | 表象 | 首轮误诊 | 真实根因 |
|---|---|---|---|
| Gate | `POST /earn/uni/lends` 前余额读 404 | 「账户族不对/期货权限缺」 | 凭据 `base_url` 已含 `/api/v4`，rail 又拼一段 → `…/api/v4/api/v4/futures/usdt/accounts` 双前缀 |
| OKX | `POST /finance/savings/purchase-redemption` 403 | 「赚币 scope 未勾」业主折腾权限两拍 | 端点错拼——文档原路径字面是截断形 `purchase-redempt`，想当然补全成 `-redemption` |

## 关键判据

**网关级拒绝不经业务校验层**，两种 4xx 都是 URL 未命中而非权限拒：

- Gate 未命中路径 → 回 **HTML 404**（Gate 业务拒回 JSON `{"label":…}`——响应体是 HTML 即说明没过应用层）
- OKX 未命中路径 → 回 **JSON 403** `{"code":403,"msg":"Forbidden"}`（OKX 业务权限拒回 5xxxx 业务码；裸 403 = 网关层）

**最快定案器 = 签名探针发空 body**：
- `POST 真端点 {}` → `400 50014 Parameter side can not be empty`（**过了权限关到业务校验**=路径真、权限通）
- `POST 错端点 {}` → `403 Forbidden`（**权限关都到不了**=路径假）
- 一次调用即分离「路径层 vs 权限层」，比让业主勾权限快一万倍。

## 教训三钉

1. **venue 4xx 第一动作是拿文档逐字符对路径**——`purchase-redempt` 这种截断命名是 venue 真路径，IDE 式补全直觉会杀人。GET 通 POST 拒 ≠ 写权限缺，也可能是俩路径一个对一个错。
2. **「业主权限没勾对」是高成本假设**——业主勾了还 403 的每一次都是证据冲突信号，该立刻回头查自身代码而非反复推业主件。
3. **签名探针是权限诊断唯一真话源**——SG `tools/probe_okxearn/`（非跟踪面）用容器内 master key 解密凭据签名直打 venue，空 body 即分层定界；凭据明文不落会话。

## 修复锚

- gate：`gateAPIBase` 剥尾部 `/api/v4` 归一（earn_rail_gate.go，@a24df0e2）
- okx：`purchase-redemption`→`purchase-redempt`（earn_rail.go Subscribe 两处，收口三件票）
