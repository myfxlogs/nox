---
schema_version: 2
id: dryrun-probe-behind-write-gates
kind: pitfall
risk: medium
status: verified
keywords: [dry_run, 试算, 裸探针, 前置校验, 写路径闸, 缺口钮死路, approve]
scope: [internal/dashboard, approve/RPC, 前端试算面]
summary: dry_run/体检探针请求排在写路径前置校验（入参必填闸/接线 nil 检查）之后=探针死路——前端挂载探针永不成功、逐键试算被无关接线状态拒绝；豁免口径必须「零副作用验证过的请求才放行」。
first_seen: 2026-09-25
last_verified: 2026-09-25
skills: [事实核验]
evidence_refs:
  - type: decision
    ref: D-490
  - type: decision
    ref: D-492
  - type: commit
    ref: d149563b
  - type: commit
    ref: 2facb440
---

## 事实/症状

D-490 授权 dry_run 排在 `xassetReloader == nil` 检查之后——前端逐键触发的试算在重载器未接线环境全部被拒（试算本不依赖写库路径）。D-492 R1：approve 弹窗挂载裸探针（只取 venue_gap_keys）撞 `allocModeInputs`「三 cap 必填」校验，perp_perp 缺口补开钮永不渲——死路在自报遗留才暴露。

## 根因或策略解释

校验栈按「真授权」一条路径设计，dry_run/探针是后加的旁路；入口校验不区分意图，把「无副作用读」挡在「写路径必需前置条件」之外。写库闸（reloader/显式必填）对探针既非必要也非充分——探针要的只是读侧凭据（gap keys/体检字段）。

## 处置

- 校验分流先做意图判断：`dry_run && 无 sizing/显式入参` → 裸探针豁免放行（`allocModeInputs` 返 err=""）。
- dry_run 短路点放在一切写路径依赖检查之前（reloader nil、落账、热重载、告警）。
- 前端探针回包只取需要的字段入独立态，不污染主面板门控（est 仍 allocOk 门）。

## 证据

- check: `TestD492_R1_BareProbeGapKeys`（perp_perp+spot_carry 裸 dry_run→ok+venue_gap_keys+零写库断言）
- result: 66 测 race×3 绿；934 测真 DSN 绿
- ref: commit 2facb440；d149563b（dry_run 前置短路修）

## 反证条件

- check: 若 dry_run 裸探针被发现可触发任何写库/状态变更
- falsifies_when: 豁免路径出现副作用（落账/告警/快照写入）=豁免口径过宽须收回

## 适用边界

只适用于「零副作用读侧探针」；凡涉及状态写入/热重载的 preview 不适用。豁免判据必须是「无入参」而非「dry_run=true」单条件——带显式入参的 dry_run 仍走全量校验。
