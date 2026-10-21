# funding regime 腰斩衰减（2026-10-16 · #147 摘牌实证）

**情境**：#147 XPD 纸面观测窗内 funding_ann 33.7%→16.8% 衰减中，accrued +1.49 未盖费 −7.08，净额靠收敛价差 +9.40 顶正。
**实数**：毕业闸改判据后自动摘牌「息差未盖费（accrued 1.49<fee 7.08）」。
**根因**：funding 尖峰瞬时读数≠可持续 regime——观测窗内已腰斩的利率继续按入场值外推=自欺。
**判据挂接**：EA-06（持续性窗统计非瞬时值）+EA-03（息差独立盖费）。
**回改点**：kill_trigger:funding_carry（regime 衰减=持续性窗统计非瞬时值——armed 径每拍重查兜底在案）
**证据**：SG fin_carry_graduation #147 行字段实拍。
**处置**：判据已收紧（@df5190f91）；armed 径 regime 持续闸每拍重查兜底。
