# decisions — 决策记录

> 一行一决策：编号 · 日期 · 结论 · 理由（一句）。顺序取号；引用旧编号写 `旧D-xxx`。
> D-001~D-211 出处=归档仓 `/opt/arb-archive/docs/handoff/decisions.md`（只读）；D-212 起续号。

| D# | 日期 | 结论 | 理由 |
|---|---|---|---|
| D-212 | 2026-10-12 | **归档重建总裁定（业主全权授权决策席）**：旧仓 `/opt/arb`→`/opt/arb-archive` 只读归档（hotfix-only 至切流），新项目仍名 `arb`（module/目录不变）按 `docs/公司架构.md` 十一部目录重建；**搬迁不重写**（包带测试整搬、filter-repo 保史）、入场有门（部门目录+dNNN 改名+D# 注释清+模块页+import 红线）、数据一行不动（`0001_baseline.sql`=生产 schema dump）、纪律原样继承（AGENTS §2–§8 全文、hooks/allowlist/skills 逐字节、D# 续号）；**不搬**=CMC 面/price_alerts/investment_records/INTRA 沙箱/capital 4 空表（归档可取，反裁走 D#）；TRX 托管资产线搬（D-066 账本隔离不变）。票序 M0 归档→M1 骨架→M2.1–2.6 按部门搬→M3 对等闸+切流→M4+ 六新件；裁决面=`docs/搬迁清单.md`。 | 收益=甩非基金业务约三成代码+文档债归零；代价=双轨期 hotfix 移植；XAU carry 休眠、仅 DCI PUT 线在跑=盈利验证低谷期，机会成本最低。 |
| D-212R1 | 2026-10-12 | **GitHub 复用不改名**（业主令「可清空」）：`myfxlogs/arb` 继续作新仓远端；旧历史保全=M0 先推 `archive/2026-10-12` 分支+tag 并 `ls-remote` 核在，M1 首推新 master 一次性 `--force`（唯一例外，此后 ff-only 不变）；本机 `/opt/arb-archive` 为第二副本，备份三落点 ③ 不缺位。业主同时明示：**两席位工作方式保留**（`[角色:…]` 开工指令、`[施工完成:<票号>] @<hash>` 尾行、实施≠复审）——随 AGENTS §3/§4 原文搬入。业主条件请求清零。 | 原裁定要业主改名+建仓两动作；业主答「可清空」=授权覆盖远端 master，但代码远端副本是备份落点之一不得真清——以同仓归档分支替代改名，零业主动作且不丢落点。 |
