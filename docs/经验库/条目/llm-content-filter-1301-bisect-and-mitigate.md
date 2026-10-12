---
schema_version: 2
id: llm-content-filter-1301-bisect-and-mitigate
kind: pitfall
risk: medium
status: verified
keywords: [1301, 内容安全, invalid_request_error, API 400, LLM 拒绝, 部门日报, 二分定位, 币安, 冻结]
scope: [deptreport, analyst, LLM 网关, 中文内容过滤, 生成报告面]
summary: 中文 LLM 网关 1301 内容闸三层触发——CJK 硬词、敏感语义对（冻结+金额）、同源复述量累计；二分定位+脱敏/缩量/重试三件套解。
first_seen: 2026-10-13
last_verified: 2026-10-13
skills: [生产取证]
evidence_refs:
  - type: commit
    ref: 1b81b792
  - type: server_fact
    ref: "SG probe1301 二分实证：币安行情单行硬触发；研究L6×账本L5/L7 软触发；十部全文重试4连挂/300runes头尾紧凑放行"
---

# LLM 网关 1301 内容闸——三层触发与三件套缓解

## 症状

部门日报首班（07:05 BJ）：数据部 + manager 两路 LLM 调用 `stream round 0: API 400`，
body 含 `code:"1301"`「系统检测到输入或生成内容可能包含不安全或敏感内容」。
其余 10/11 部报同构输入正常——触发是内容相关非配置相关。

## 根因（二分定位实证）

**判分是概率模型非关键词表**——同输入两次可一挂一过（边界抖动），但有三层触发：

| 层 | 实证 | 性质 |
|---|---|---|
| CJK 硬词 | 「币安行情」单行即挂；「币安」「binance」单独放行 | 确定性硬触发 |
| 语义对 | 「疑似冻结报价」×「USD 金额行」并置挂；各自单发放行 | 资金冻结诈骗语境误伤 |
| 复述量 | 十部正文重复同一告警簇 ~4.5KB 累计越线：重试 4 连挂、行级 dedup 仍挂 | 确定性超载 |

定位法=一次性 probe 工具二分（单报→对→行→token 摘除法），勿猜词表。

## 缓解（deptreport 三件套，`1b81b792`）

1. **scrub 入文/出文脱敏**：`币安→binance`、`冻结→停滞`——新触发词按同径补映射，不臆测扩词
2. **compact 缩量**：manager 输入单部正文 ≤300 runes 头 2/3+尾 1/3 行界截断（保①状态③待办）
3. **单重试**：边界抖动同文重发即过；系统提示补「venue 名用英文原词」防输出侧自发译名（1301 输出侧也拦）

fail-open 不动——二次仍挂=当班跳过非事故。

## 证据

- check: SG `probe1301 -mode <mode>`（tools 一次性件用后删）——单报/对/行/token/截断/重试各面
- result: 见根因表；补班 Generate 11/11 全成+manager 1048B 落库+全表零触发词
- ref: commit 1b81b792

## 反证条件

- check: 新班再出 1301
- falsifies_when: scrub/compact 后仍确定性复现 → 存在第四层触发，重开二分

## 适用边界

- 适用=经同一中文网关的所有 LLM 调用面（deptreport/explorer/sysretro/digest 共用上游）；
  他面复现 1301 先套同径二分再补映射。
- 脱敏只洗 LLM 绑定文本，DB 原值不动；勿在源头数据改名迎合过滤器。
