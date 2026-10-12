package autotrade

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/risk/gate"
	"arb/internal/store/pgstore"
)

// risk_lines.go —— D-032（decisions D-030 风控硬线）：
// ① 30d 滚动账本回撤自动全停（post-settle 钩子逐笔评估）；
// ③ 权益纪律入场闸（venue 权益 ≥ pct%×(该 venue 在仓名义 + 本次名义)，快照缺/超龄 fail-closed）。
// ② 事故仓停新开在 gate_incident.go（gate.Gate.Submit 内联闸）。

// riskDD30Window 30 天滚动窗（D-030：30d rolling net loss ≤ -3% 全停）。
const riskDD30Window = 30 * 24 * time.Hour

// riskEquitySnapshotMaxAge 权益快照新鲜度（同 tickFreshWindow 报价 5min 口径；
// 超龄=数据不可信 fail-closed）。
const riskEquitySnapshotMaxAge = 5 * time.Minute

// riskEquityTypes 权益纪律闸适用类型与「该 venue 在仓名义」计数域
// （D-030 硬线口径：XASSET/FUNDING_HEDGE 在仓名义）。
var riskEquityTypes = []string{"XASSET_CARRY", "FUNDING_HEDGE"}

// postSettleRiskCheck settlement.PostSettler 挂点：结算行落账后评估 dd30 线。
func (e *Engine) postSettleRiskCheck(ctx context.Context, _ int64) {
	RiskDD30Check(ctx, e.store, e.gate.ControlPlane(), e.cfg.RiskDD30USD)
}

// RiskDD30Check 30 天滚动账本净额 ≤ 阈值 → 自动 KillSwitch。
// 全样本（含事故仓，风控线不剔样本，excludeIncidents=false）。
// 幂等：KillSwitch 已激活不重复触发（execution_controls 终态行不刷事件）。
// fail-closed：账本查询失败 → 视同不可证明在线内 → best-effort 触发停机（
// trigger 自身失败只留日志——DB 都不可达时执行面亦无法动作）。
func RiskDD30Check(ctx context.Context, st *pgstore.Store, cp *gate.ControlPlane, thresholdUSD float64) {
	if st == nil || cp == nil {
		return
	}
	net, err := st.NetRealizedSince(ctx, time.Now().Add(-riskDD30Window), false)
	if err != nil {
		log.Printf("[risk] dd30 eval failed: %v → fail-closed trigger", err)
		if terr := cp.TriggerKillSwitch(ctx, "risk-dd30", fmt.Sprintf("dd30 ledger eval failed: %v", err)); terr != nil {
			log.Printf("[risk] dd30 kill trigger failed: %v", terr)
		}
		return
	}
	if net > thresholdUSD {
		return
	}
	if active, aerr := cp.IsKillSwitchActive(ctx); aerr != nil {
		log.Printf("[risk] dd30 kill state check: %v (skip trigger, eval net=$%.2f)", aerr, net)
		return
	} else if active {
		return // 已停机：幂等不重复触发
	}
	if err := cp.TriggerKillSwitch(ctx, "risk-dd30",
		fmt.Sprintf("30d rolling ledger net $%.2f <= limit $%.2f (D-030 hardline)", net, thresholdUSD)); err != nil {
		log.Printf("[risk] dd30 kill trigger (net=$%.2f): %v", net, err)
	}
}

// equityDisciplineGate D-032 ③：权益纪律入场闸（econEntryGate 之后同一处，engine_opp）。
// 返回 true=拒开。适用 XASSET_CARRY/FUNDING_HEDGE；每腿 venue 独立判：
// equity ≥ pct%×(venue 在仓名义 + 本次腿名义)。快照缺/超龄/查询败 → fail-closed 拒开。
// pct≤0=该侧闸关（配置逃生口，非缺省路径）。
func (e *Engine) equityDisciplineGate(ctx context.Context, opp pgstore.Opportunity, legNotionalUSD float64) bool {
	if opp.Type != "XASSET_CARRY" && opp.Type != "FUNDING_HEDGE" {
		return false
	}
	venues := []string{opp.VenueBuy}
	if opp.VenueSell != opp.VenueBuy {
		venues = append(venues, opp.VenueSell)
	}
	needPct := false
	for _, v := range venues {
		if gate.IsMTVenue(v) {
			needPct = needPct || e.cfg.RiskCfdEquityPct > 0
		} else {
			needPct = needPct || e.cfg.RiskCexEquityPct > 0
		}
	}
	if !needPct {
		return false
	}
	expo, err := e.store.VenueNotionalExposure(ctx, riskEquityTypes)
	if err != nil {
		log.Printf("[autotrade] skip %s: venue exposure query %v (权益纪律 fail-closed 拒开)", opp.ID, err)
		return true
	}
	for _, v := range venues {
		pct := e.cfg.RiskCexEquityPct
		if gate.IsMTVenue(v) {
			pct = e.cfg.RiskCfdEquityPct
		}
		if pct <= 0 {
			continue
		}
		eq, _, asOf, ok := e.service.AccountEquityUSDAt(v)
		// MT5 账户权益唯一流源=OnOrderProfit 帧，零持仓时 venue 不推帧
		// （10-05 实拍：平掉最后一仓后 mt_frame 零行）——空账户第一笔开仓前在判定点
		// 同步 REST 直读 venue 当前值一次（非快照、非缓存，与 MarginPrecheck 同类；
		// 零持仓无帧为 venue 行为），读败仍 fail-closed。
		// CEX 腿有私有流账本，不走直读。
		refreshed := false
		if gate.IsMTVenue(v) && (!ok || time.Since(asOf) > riskEquitySnapshotMaxAge) {
			e.service.RefreshAccountEquity(ctx, v)
			refreshed = true
			eq, _, asOf, ok = e.service.AccountEquityUSDAt(v)
		}
		switch {
		case !ok:
			if refreshed {
				log.Printf("[autotrade] skip %s: equity snapshot missing @%s (直读后仍缺·权益纪律 fail-closed 拒开)", opp.ID, v)
			} else {
				log.Printf("[autotrade] skip %s: equity snapshot missing @%s (权益纪律 fail-closed 拒开)", opp.ID, v)
			}
			return true
		case time.Since(asOf) > riskEquitySnapshotMaxAge:
			if refreshed {
				log.Printf("[autotrade] skip %s: equity snapshot stale %s @%s (直读后仍超龄·权益纪律 fail-closed 拒开)", opp.ID, time.Since(asOf).Round(time.Second), v)
			} else {
				log.Printf("[autotrade] skip %s: equity snapshot stale %s @%s (权益纪律 fail-closed 拒开)", opp.ID, time.Since(asOf).Round(time.Second), v)
			}
			return true
		}
		need := (expo[v] + legNotionalUSD) * pct / 100
		if eq < need {
			log.Printf("[autotrade] skip %s: equity $%.0f < %.0f%%×(在仓$%.0f+本次$%.0f)=$%.0f @%s (权益纪律拒开)",
				opp.ID, eq, pct, expo[v], legNotionalUSD, need, v)
			return true
		}
	}
	return false
}
