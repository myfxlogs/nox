---
schema_version: 2
id: stream-payload-tap-discard-then-reconstruct
kind: pitfall
risk: high
status: observed
keywords: [流事件, 消息体, 序列化, 纯标量tap, Deal, DealType_Balance, 差分重建, 能力边界, 未亲证断言, proto, OnOrderUpdate, 入金捕获]
scope: [供应商 API/gRPC 面能力结论, 流事件接入设计, tap/缓存层设计, 任何「venue 不支持 X」类断言]
summary: 断言「流里没有 X 事件」却从未读过流消息体——mt5.proto 的 OrderUpdate 事件自带全套序列化 Deal（DealType_Balance=入金/Credit/Charge/Bonus 全标签+票号+金额字段），现行 tap 为免引 pb 类型只提六个标量把载荷丢弃，随后三轮差分机（三扣+成熟度+宽限）重建被自己丢弃的信息。能力结论必须以消息体全文为证，非 RPC 名单面、非他人转述。
first_seen: 2026-10-17
last_verified: 2026-10-17
skills: [协议面取证]
evidence_refs:
  - type: decision
    ref: D-607
  - type: code
    ref: proto/mtapi/mt5/mt5.proto OrderUpdate/DealInternal/DealType（2697-2713/1048-1100/1015-1029）
  - type: code
    ref: internal/api/dashboard/account_stream.go:40（纯标量 tap 丢弃点）
fix_and_prevention: ①下「无 X 能力」结论前先读 proto/SDK 的消息体定义——RPC 名单≠能力面，拉取面与流面分开核；②tap 层禁为类型洁癖丢载荷——需要透传用值对象/回调（BindMTEquityFn 范式），丢弃=永久信息损失；③「供应商/流不推 X」类断言须帧级实证（日志/探针/落库行），禁采信未验证转述（本案两句被采信断言均错）；④重建/推断机器立项前先查「要重建的信息是否在被自己丢的载荷里」。
notes: 业主 10-17 定谳「低级错误」并连环纠偏两轮（D-606 能力边界→D-607 主径翻案）；同族检查=api-response-fields-empirical（响应字段想当然）——本条是其流面镜像；衍生技能=skills/协议面取证。
---
