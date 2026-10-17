---
name: mt5-account
description: mtapi MT5 账户面 RPC——Account/AccountSummary/ServerTimezone/RequiredMargin/Mails 实测能力与坑
---

# mt5 账户面（实测 2026-10-17，证据=docs/能力账/mt5-rpc-probe-2026-10-17.json）

| RPC | 功能 | ICR 15032313 | EXR 3001157 | XMR 110660304 | 载荷关键字段 |
|---|---|---|---|---|---|
| Account | 登录户全档（login/balance/credit/leverage） | ok | ok | ok | result.Balance/Credit/Leverage（Login uint64） |
| AccountSummary | 实时 equity/free_margin | ok | ok | ok | result.Equity/FreeMargin——mt_frame/capitalview 源 |
| ServerTimezone | 服务端时区 offset（小时，含 DST） | ok | ok | ok | result（double）——D-337 swap 计费零点/brokertz 缓存源 |
| RequiredMargin | 挂单保证金试算 | ok | err: Symbol not exist | err: Symbol not exist | result（double）——**符号方言坑**：EXR/XMR 户 XAUUSD 裸名不存在（XAUUSDm 型尾缀），须 symMap 原生名 |
| Mails | 信箱列举 | Unimplemented | Unimplemented | Unimplemented | 网关未实现（三户一致定谳）；流侧 OnMail 同 Unimplemented |

**坑**：①token 走**请求体 Id 字段**（每 Request 必填）——metadata header 径网关不读（首版 harness 全 RPC "Value cannot be null" 实拍）；②Account 无参但 Id 必填；③ServerTimezone 返回 FixedZone 可用 offset 非 tz 名（DST 由 24h TTL 重取自愈，D-337）。
**复跑**：`go run ./tools/mtprobe --dsn <dsn> --out docs/能力账`（SG）。
