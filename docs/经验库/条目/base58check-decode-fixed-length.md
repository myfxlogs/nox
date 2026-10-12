---
---
schema_version: 2
id: base58check-decode-fixed-length
kind: pitfall
risk: medium
status: verified
keywords: [base58, base58check, TRON, 地址, hex, 解码, 定长, 前导零, 校验和]
scope: [TRON, base58 系地址(Solana/Bitcoin 同族), 地址比对工具代码]
summary: base58↔hex 互转比对必须定长解码——TRON 地址 base58check 应固定解码为 25 字节(0x41+20B payload+4B 校验和)再取前 21 字节比对;可变长 int→bytes 会因前导零/校验和边界产生 21/25 字节两种形态,拼入比对时"永不匹配"。两次实际踩坑:①hexOf 把 4 字节校验和拼进 payload(50 字符 vs 42 字符)误判"所投 SR 不在 listwitnesses 445 条中";②Tronscan 返回 base58 而用 hex 比对落空。修法=to_bytes(25,'big') + assert b[0]==0x41 + 取 b[:21]。
first_seen: 2026-09-03
last_verified: 2026-09-03
skills: []
evidence_refs:
  - type: doc
    ref: docs/TRX质押-P0侦察报告-2026-09-03.md §6.5
  - type: doc
    ref: docs/D-210侦察报告审计-2026-09-03.md §2#5
  - type: decision
    ref: D-210
---

## 事实/症状

同一会话两次因 base58/hex 转换形态不一致得出假阴性结论:①自写 hexOf 用 `int.to_bytes((bit_length+7)//8)` 解码后把全量字节(含 4 字节校验和)与 witness 42 字符 payload 比对 → 误判"业主所投 SR 不在 listwitnesses"(实际在任,isJobs=true);②对 Tronscan API 返回的 base58 地址用 hex 常量比对 → 命中数为 0。

## 根因或策略解释

base58check 解码的输出长度依赖数值高位零字节与是否剥离校验和,不固定长度就存在 21 字节(payload 形态)与 25 字节(全量形态)两种"都对"的表示;跨数据源比对时一方 21 方一方 25(或 42 vs 50 hex 字符),相等判断恒假,且不报错、难察觉。

## 处置

统一解码器:按目标链定长解码(TRON=25 字节),`to_bytes(25,'big')` 显式定长,`assert b[0]==0x41`,payload 取 `b[:21]`;对外比较一律 payload 形态(41+20B hex,42 字符);比对前打印双方长度做单元断言。TRON 官方校验向量:TNeEwWHXLLUgEtfzTnYN8wtVenGxuMzZCE ↔ 418b0359acac03bac62cbf89c4b787cb10b3c3f513。

## 证据

- check: 修正后解码器回归——业主/SR/受托方三地址与 listwitnesses、链上交易 raw_data 中的 hex 地址互查
- result: SR 命中 listwitnesses(排名 24/445 复核一致),受托方/每日付款方判定与前次相反结论,审计全史复验无新矛盾
- ref: D-210(报告 a606951、审计 d1839eb)

## 反证条件

- check: 用 TRON 主网任意 10 个地址做 base58↔hex 往返回归(定长解码器)
- falsifies_when: 任一标准地址在定长解码下 assert 失败或往返不一致,则本修法有误

## 适用边界

base58check 族地址(TRON/Solana/Bitcoin);因数据源可能各自返回 base58 或 hex,跨源比对的转换责任在本方代码。可泛化至任意 base58 比对场景——晋升候选(通用编码坑,与项目域无关)。
