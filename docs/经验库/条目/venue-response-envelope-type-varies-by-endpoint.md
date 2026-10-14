---
schema_version: 2
id: venue-response-envelope-type-varies-by-endpoint
kind: pitfall
risk: high
status: verified
keywords: [OKX, code 字段, 数字 vs 字符串, sfp/dcd, unmarshal, 应答 envelope, 端点分族]
scope: [dciexec, cexscan, okx rail, venue 客户端]
summary: OKX sfp/dcd 族应答 code 是数字 0 而 asset/account/trade 族是字符串 "0"——envelope 按 string 断言致 dcd 全族 unmarshal 灭；字段类型按端点实拍钉，envelope 用 RawMessage 宽容收。
first_seen: 2026-10-14
last_verified: 2026-10-14
skills: [生产取证]
evidence_refs:
  - type: commit
    ref: 6fc0fecf
  - type: server_fact
    ref: "SG nox-core 16:33-16:40 [dci-settle] okx order 30: cannot unmarshal number into Go struct field .code of type string（每拍复发）"
  - type: commit
    ref: 6a8709c8
---

# venue 应答 envelope 字段类型按端点分族——别按一个端点的实拍钉死类型

## 病灶链

`okxEarnRail.req` 的应答 envelope 声明 `Code string`，OKX `/api/v5/finance/sfp/dcd/*` 族端点（products/order-status）实拍回**数字** `code:0` → unmarshal 全灭，settle 轮询每拍 `[dci-settle] okx order 30: …cannot unmarshal number…` 报错。采集件 `dci_okx.go` 早用 `json.Number` 收过同款坑（注释有实证），执行轨另写 envelope 时没复用教训——同一 venue 两个客户端两处各咬一次。

## 根因

同一 venue 不同端点族应答 schema 不一致（金融产品线各自实现）。mock 写 `"code":"0"` 把假想类型钉进断言——测试绿生产红，同 mock 钉错病灶家族。

## 处置

envelope `Code` 改 `json.RawMessage` 收后 `strings.Trim(code,'"')` 归一再比 `"0"`——数字/字符串两形通吃；错误码 `51000`/`50011` 照常进 err 径。写新 venue 端点客户端一律宽容收 envelope 字段。

## 证据

- check: SG `docker logs nox-core | grep "order 30"` → unmarshal number into string 每拍复发；修后部署 `1592eb5b` 零复发。
- result: `TestOkxSettle_EarnRowSkipped` mock 用 `{"code":0,…}` 数字形钉容错。
- ref: `6fc0fecf`（envelope 容错）、`6a8709c8`（到期闸）。

## 反证条件

- check: OKX 官方文档声明 sfp/dcd code 恒为字符串且生产实拍复核。
- falsifies_when: venue 回包数字 code 消失或统一字符串——则容错可退化为 string 直收（宽容件留着也无害）。

## 适用边界

OKX 已实证；其他 venue 同理推断但未经各自端点实拍勿外推为定论。同族教训：Go `time.Parse("060102",…)` 两位年 69-99 映 19xx——`991231` 是 1999 不是 2099，测试锚选日期时注意。

## 关联

- `venue-4xx-check-url-spelling-before-blaming-permissions`——同「想当然外推+mock 钉错」家族。
- `api-response-fields-empirical`——字段名以实拍为准的同源判据。
