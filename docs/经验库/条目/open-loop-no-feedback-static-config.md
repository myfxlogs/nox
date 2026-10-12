---
schema_version: 2
id: open-loop-no-feedback-static-config
kind: pitfall
risk: medium
status: observed
keywords: [开环控制, 静态配置, 白名单, 无复活, 无校验, 无自我审计, 被拒机会, 影子跟踪, gate 有效性, review 不回写]
scope: [autotrade, config, gate, review, 全系统]
summary: 系统仍是开环的——多数配置按治理标准无自动校准/复活，被拒机会虽已有运行日志但无持久影子跟踪，review 不回写，策略淘汰只 disable 不 enable；无反馈回路判断"你拒对了还是拒错了"。
first_seen: 2026-09-02
last_verified: 2026-09-02
skills: [事实核验]
evidence_refs:
  - type: doc
    ref: internal/config/config.go:215-294
  - type: doc
    ref: internal/config/config_autotrade.go:47-64
  - type: doc
    ref: internal/autotrade/engine_opp.go:48-73
  - type: doc
    ref: internal/review/
---

## 事实/症状

D-188 地基审计复核业主提出的 3 项问题，并确认这是同一类根因——**开环控制**：

### 1. 白名单死、无复活机制
- 多数 env 配置按本审计标准属于"死/半活"——启动时加载，缺少自动校准或复活；这是治理分类，不代表每个静态阈值本身错误
- `ExcludeCanonicals`（BRENT/WTI）、`SwapArbDenyCombos`、`perpFundingWhitelist` "加了就不删"
- `AutoTradeAllowVenues` 默认仅 2 个 venue，改 env 需重启
- 无自动校准（review 只出建议不回写 Config）
- 无过期/复审机制

### 2. 对机会无校验机制
- 被门禁拒绝的机会不进入持久影子跟踪——运行日志存在，但没有跟踪后果
- 没有"如果开了会怎样"的影子跟踪
- BRENT 1314615×39050818 被拒 12 天，年化 20.7%，当时系统没有持久机制评估是否误拦
- D-187 已为 handleOpportunity 的主要拒绝分支补日志，但日志不等于反馈闭环

### 3. 无自我审计
- review 引擎产出 `knowledge_entries` 调参建议，但没有回写 Config 的机制
- 策略淘汰自动 `DisableStrategy`，但 `EnableStrategy` 只能人工调用
- `STRATEGY_EXHAUSTED`/`CLOSE_ONLY` 状态无自动恢复路径
- 系统没有能力检测"我在犯错"——靠人工审计（业主问"为什么没开仓"）

## 根因或策略解释

当前架构是开环的：
```
detector → gates(静态过滤) → 执行 → 持仓 → 平仓 → 账本
                ↑
            无反馈：被拒机会消失，不跟踪后果
```

gates 做完决策就忘了。没有反馈回路告诉它"你拒对了"还是"你拒错了"。

闭环控制应该是：
```
detector → gates(过滤) → 执行 → 持仓 → 平仓 → 账本
              ↓                                    ↓
         被拒机会跟踪 ────────────── 后果校验 ────┘
              ↓
         gate 有效性度量 → 误拦告警 / 自动复活 / 阈值校准
```

## 处置

**待修复（D-197+ 闭环反馈设计，D-189/D-190 完成后立项）**：

1. **被拒机会影子跟踪**：每个 gate 拒绝时落 `(combo, gate_name, reason, ts, 报价)` 到表，跟踪后续报价走势——如果价差持续存在，说明真实机会被误拦

2. **gate 有效性度量**：每个 gate 定期统计 false positive rate（被拒但影子跟踪显示拒错了）和 false negative rate（通过但实际亏损）→ 阈值需调松/调紧

3. **自动复活机制**：venue/combo 被排除后定期重新评估，影子跟踪期间无假机会特征则自动解除

4. **review 回写闭环**：review 建议经人工确认后自动更新 DB 配置（而非只留 knowledge_entries）

5. **策略禁用自动恢复**：禁用后定期重评估，满足条件自动 EnableStrategy

**前置条件**：D-187（统一路径 + 拒绝日志）是这些反馈回路的基础设施。

> 注：本条目 status=observed（已观察未修复），这是架构级问题，修复需分阶段（D-187→闭环反馈设计→落地），不能一次性修完。修复完成后升 verified。

## 证据

- check: `grep -rn 'AutoCalibrat\|autoRevive\|shadowTrack\|gateEffectiveness' internal/`
- result: 零命中——无任何自动校准/复活/影子跟踪/有效性度量代码
- ref: D-188 地基审计（2026-09-02）

## 反证条件

- check: 确认是否有外部脚本/cron 做配置校准或被拒机会跟踪
- falsifies_when: 如果有外部系统（如 cron 脚本）定期校准配置或跟踪被拒机会，则部分闭环（但审计未发现）

## 适用边界

这是架构级问题，不是单点 bug。修复需要分阶段：D-187（拒绝日志基础设施）→ 闭环反馈设计 → 闭环反馈落地。不能一次性修完。
