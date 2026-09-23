// service_margin.go —— D-405：marginGuard 装配 + 开户门槛判定（S4）+ RPC 数据源（S6）。
package dashboard

import (
	"context"
	"fmt"
	"time"
)

// InitMarginGuard 装配保证金三档评估器：注册 snapshot 写口钩子（事件驱动评估，
// 三写口收敛单钩）。在 store/service 就绪后调用一次（main_monitors 装配点）。
func (s *Service) InitMarginGuard(urgentCD, warnCD time.Duration) {
	g := NewMarginGuard(s.store, s.qbus, s.legContractSizeOf, urgentCD, warnCD)
	s.marginGuard = g
	s.store.SetVenueSnapshotHook(g.OnSnapshots)
}

// MarginFloorCheck D-405 S4 开户门槛档判定（autotrade.Gate 注入点）。
// 口径：venue 可用额 < 新仓所需保证金 → false。
//   - 可用额：MT=free_margin_raw；CEX=equity−MMR（buffer_multiple 归一）。
//   - 所需保证金：addNotionalUSD × 隐含保证金率（=该 venue 当前已用保证金/ΣOPEN 腿名义额，
//     venue 自适应——不引入拍脑袋杠杆常量；无 OPEN 腿 venue 无隐含率可算 → 放行跳过）。
//
// 数据缺（无快照行/字段未采/隐含率不可算）→ ok=true + detail 说明（监控件 fail-open，
// 不阻断开仓链——开户门槛语义=资金够才开，证明不了不够不拦）。
func (s *Service) MarginFloorCheck(ctx context.Context, venueKey string, addNotionalUSD float64) (bool, string) {
	venue, vt := legSnapshotVenue(venueKey)
	if marginVenueClass(vt) == "nonMargin" {
		return true, "non-margin venue" // 现货/资金账户无保证金概念（SSOT §1）
	}
	snap, ok, err := s.store.LatestVenueAccountSnapshot(ctx, venue, vt)
	if err != nil {
		return true, fmt.Sprintf("snapshot query err %v（fail-open）", err)
	}
	if !ok || snap.EquityRaw == nil {
		return true, "no account snapshot" // 未采 venue 无从判定
	}
	legs, err := s.store.ListOpenLegsForMargin(ctx)
	if err != nil {
		return true, fmt.Sprintf("open legs err %v（fail-open）", err)
	}
	var sumNotional float64
	for _, l := range legs {
		lv, lt := legSnapshotVenue(l.Venue)
		if lv != venue || lt != vt {
			continue
		}
		if n, nok := marginLegNotionalUSD(s.qbus, s.legContractSizeOf, l); nok {
			sumNotional += n
		}
	}
	if sumNotional <= 0 {
		return true, "no open legs (implied margin rate unknown)"
	}
	var avail, used float64
	if marginVenueClass(vt) == "cfd" {
		if snap.FreeMarginRaw == nil {
			return true, "free_margin not collected"
		}
		avail = *snap.FreeMarginRaw
		used = *snap.EquityRaw - avail
	} else {
		if snap.BufferMultiple == nil || *snap.BufferMultiple <= 0 {
			return true, "buffer_multiple not collected"
		}
		mmr := *snap.EquityRaw / *snap.BufferMultiple
		avail = *snap.EquityRaw - mmr
		used = mmr // IMR 未知，以 MMR 为隐含率锚（保守：MMR≤IMR → 低估所需=偏放行，监控件口径）
	}
	if used <= 0 {
		return true, "used margin <=0 (implied rate unknown)"
	}
	required := addNotionalUSD * used / sumNotional
	if avail < required {
		return false, fmt.Sprintf("avail $%.2f < required $%.2f（隐含率 %.1f%% × 新仓 $%.2f）",
			avail, required, used/sumNotional*100, addNotionalUSD)
	}
	return true, fmt.Sprintf("avail $%.2f >= required $%.2f", avail, required)
}
