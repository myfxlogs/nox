---
schema_version: 2
id: dust-amount-below-venue-min-transfer
kind: pitfall
risk: medium
status: verified
keywords: [尘额, 划转, min()语义, amt error, 短差, OKX transfer, 51000]
scope: [dciexec, earn_rail, 账户划转, 金额边界]
summary: 「差多少补多少」的 min() 划转语义在尘额场景产零值/纳级请求——`needed=3.4e-9` 时 `min(needed,unified)` 触发 `amt=0.00000000` 划转被 venue 51000 拒；尘级短差应直接按现有资金实额操作，真短差才走划转。
first_seen: 2026-10-14
last_verified: 2026-10-14
skills: [生产取证]
evidence_refs:
  - type: commit
    ref: 0efa0771
  - type: commit
    ref: 0f9e2fb8
  - type: server_fact
    ref: "SG 订单 #22/#24：okx 18→6 划转 amt=0.00000000 → venue HTTP 400 51000 Parameter amt error"
---

# 「min(短差, 余额)」划转语义在尘额下产废单——三条边界要分清

## 病灶链

申购金额口径 `amount = unified + funding`（合并余额），划转步算 `needed = amount − funding`。
当 unified 只剩尘额时 `needed = 3.4e-9`：

- 旧 `mv = min(needed, unified)` → `mv=3.4e-9 > 0` → `FormatFloat(mv,'f',8)` 截成 `"0.00000000"` → venue `51000 Parameter amt error`。
- 中间版「`unified < needed` 则 fail-closed」对**真短差**正确，但尘级 needed 仍会走到划转分支照样废单。
- 正解三态：`needed ≤ 尘阈(1e-6)` → 零划转按 funding 实额购；`needed > 尘阈 ∧ unified ≥ needed` → 划全差；`unified < needed` → fail-closed 如实拒。

## 判据

凡是「按比例/差额算划转额」的代码路径，金额写到请求体前必须过两道：**①venue 可表达精度（8 位截断后非零）②venue 最小划转面额**。两条不过的差额不是「差一点」，是「没有」——要么按现有资金实额走，要么 fail-closed，禁发注定失败的请求。

## 同族信号

- venue 返回 `Parameter amt error`/`INVALID_PARAM_VALUE amount` 且业务侧金额经多层浮点合成 → 先怀疑精度截断产零值，不是权限也不是余额。
- 金额来源是 `a+b−c` 这类合成差值时，浮点尾差本身就会造尘级 needed（`unified+funding` 读出时间差/精度差即可让 needed≠unified）。
