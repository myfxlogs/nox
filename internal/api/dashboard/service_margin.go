// service_margin.go —— D-405：marginGuard 装配 + 开户门槛判定（S4）+ RPC 数据源（S6）。
package dashboard

import (
	"context"
	"fmt"
	"time"

	"nox/internal/portfolio/capitalview"
	"nox/internal/store/pgstore"
)

// InitMarginGuard 装配保证金三档评估器：注册 snapshot 写口钩子（事件驱动评估，
// 三写口收敛单钩）。在 store/service 就绪后调用一次（main_monitors 装配点）。
func (s *Service) InitMarginGuard(urgentCD, warnCD time.Duration) {
	g := NewMarginGuard(s.store, s.qbus, s.legContractSizeOf, urgentCD, warnCD)
	s.marginGuard = g
	recon := NewResidualRecon(s.store) // D-536②：残差对账搭车同一 snapHook（异步不阻塞写径）
	s.store.SetVenueSnapshotHook(func(ctx context.Context, snaps []pgstore.VenueAccountSnapshot) {
		g.OnSnapshots(ctx, snaps)
		recon.OnSnapshots(ctx, snaps)
	})
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
	ok, _, detail := s.marginFloorEval(ctx, venueKey, addNotionalUSD)
	return ok, detail
}

// MarginFloorKnown D-406 R0-B1 轮转资金前置闸：与 MarginFloorCheck 同判定，
// 但区分「证明够」与「数据缺」——known=false=数据缺（fail-open 分支），
// 轮转闸据此 fail-closed 不轮转（踏空防线的资金半边）。
func (s *Service) MarginFloorKnown(ctx context.Context, venueKey string, addNotionalUSD float64) (ok, known bool, detail string) {
	return s.marginFloorEval(ctx, venueKey, addNotionalUSD)
}

// SetMTSummaryFunc D-563 S4b：测试注入 MT 实时资金缝（nil=回落 bm.MTAccountSummary 真身）。
func (s *Service) SetMTSummaryFunc(fn func(ctx context.Context, venueKey string) (float64, float64, string, error)) {
	s.mtSummaryFn = fn
}

// SetVenueLeverageFunc D-565 S2：测试注入 venue 杠杆实读缝（nil=venueLeverageExec 真身）。
func (s *Service) SetVenueLeverageFunc(fn func(ctx context.Context, venueKey, symbol string, sellLeg bool) (float64, error)) {
	s.venueLeverageFn = fn
}

// mtAcctSummary D-563 S4b：MT 实时资金缝（测试注入 mtSummaryFn；nil=回落 bm 真身）。
// D-565 S4：currency 随 RPC 一起实采（批钱面币种闸用——快照行退出批钱面）。
func (s *Service) mtAcctSummary(ctx context.Context, venueKey string) (float64, float64, string, error) {
	if s.mtSummaryFn != nil {
		return s.mtSummaryFn(ctx, venueKey)
	}
	if s.bm == nil {
		return 0, 0, "", fmt.Errorf("BrokerManager 未接线")
	}
	return s.bm.MTAccountSummary(ctx, venueKey)
}

// marginFloorEval 判定本体：ok=可用额≥所需；known=判定建立在采集数据上
// （false=任一 fail-open 分支：无快照/字段未采/隐含率不可算/查询错）。
func (s *Service) marginFloorEval(ctx context.Context, venueKey string, addNotionalUSD float64) (bool, bool, string) {
	venue, vt := legSnapshotVenue(venueKey)
	if marginVenueClass(vt) == "nonMargin" {
		return true, true, "non-margin venue" // 现货/资金账户无保证金概念（SSOT §1）——确定放行
	}
	// D-549 §5 安全裁定：CEX 执行链资金数=活账本/REST 实拉（capitalview.Live）；
	// REST 校准失败 fail-closed 拒开——不降级用陈旧快照开真金仓（快照兜底仅限展示面）。
	// MT venue 快照即其数据面（无私有流通道），原语义不变。
	cexLive := capitalview.IsCEXVenue(venueKey)
	var liveEq, liveAvail *float64
	if cexLive {
		f, ok := s.capitalView().LiveFresh(ctx, venueKey) // D-563 S4c：决策面强制实拉
		if !ok || !f.Live {
			return false, true, fmt.Sprintf("venue %s 活资金不可得（活账本/REST 双败，执行面禁快照兜底，fail-closed 拒开）", venueKey)
		}
		liveEq = &f.Equity
		if f.HasAvailable {
			liveAvail = &f.Available
		}
	}
	// D-563 S4b：MT 腿判定时刻资金=AccountSummary 实时 RPC（快照退决策面）；
	// RPC 败/超时（≤10s）→ fail-closed「MT 实时资金不可得」。无需快照行在档。
	if marginVenueClass(vt) == "cfd" {
		eq, fm, _, serr := s.mtAcctSummary(ctx, venueKey)
		if serr != nil {
			return false, true, fmt.Sprintf("MT 实时资金不可得（%v）fail-closed 拒开", serr)
		}
		sumNotional, legCount, skipped, err := s.marginOpenNotional(ctx, venue, vt)
		if err != nil {
			return true, false, fmt.Sprintf("open legs err %v（fail-open）", err)
		}
		if legCount == 0 {
			return true, false, "no open legs (implied margin rate unknown)"
		}
		if sumNotional <= 0 {
			return true, false, fmt.Sprintf("all %d open legs skipped（报价缺无分母，隐含率不可判）", skipped)
		}
		return marginFloorJudgeSkipped(fm, eq-fm, sumNotional, addNotionalUSD, skipped)
	}
	snap, ok, err := s.latestVenueSnapshotSafe(ctx, venue, vt)
	if err != nil {
		if cexLive {
			return true, false, "buffer_multiple 快照行查询失败（隐含率锚缺）" // live 资金已得；隐含率不可算同 fail-open
		}
		return true, false, fmt.Sprintf("snapshot query err %v（fail-open）", err)
	}
	if cexLive {
		// buffer_multiple 仅快照面有（隐含率锚，非资金数）——缺则同原 fail-open。
		if ok && snap.BufferMultiple != nil && *snap.BufferMultiple > 0 {
			return s.marginFloorCexEval(ctx, venue, vt, *liveEq, liveAvail, *snap.BufferMultiple, addNotionalUSD)
		}
		return true, false, "buffer_multiple not collected（live 资金已得，隐含率锚缺）"
	}
	if !ok || snap.EquityRaw == nil {
		return true, false, "no account snapshot" // 未采 venue 无从判定
	}
	sumNotional, legCount, skipped, err := s.marginOpenNotional(ctx, venue, vt)
	if err != nil {
		return true, false, fmt.Sprintf("open legs err %v（fail-open）", err)
	}
	if legCount == 0 {
		return true, false, "no open legs (implied margin rate unknown)"
	}
	if sumNotional <= 0 {
		return true, false, fmt.Sprintf("all %d open legs skipped（报价缺无分母，隐含率不可判）", skipped)
	}
	var avail, used float64
	if snap.BufferMultiple == nil || *snap.BufferMultiple <= 0 {
		return true, false, "buffer_multiple not collected"
	}
	mmr := *snap.EquityRaw / *snap.BufferMultiple
	avail = *snap.EquityRaw - mmr
	used = mmr // IMR 未知，以 MMR 为隐含率锚（保守：MMR≤IMR → 低估所需=偏放行，监控件口径）
	return marginFloorJudgeSkipped(avail, used, sumNotional, addNotionalUSD, skipped)
}

// marginFloorCexEval CEX 执行链判定（D-549）：equity/avail=live 实拉值；
// 隐含率锚 mmr=liveEquity/buffer_multiple（buffer 仅快照面——隐含率元数据非资金数）。
// avail=live 实采可用优先；缺→liveEquity−mmr 近似（同原快照口径）。
func (s *Service) marginFloorCexEval(ctx context.Context, venue, vt string, equity float64, liveAvail *float64, bufferMultiple, addNotionalUSD float64) (bool, bool, string) {
	sumNotional, legCount, skipped, err := s.marginOpenNotional(ctx, venue, vt)
	if err != nil {
		return true, false, fmt.Sprintf("open legs err %v（fail-open）", err)
	}
	if legCount == 0 {
		return true, false, "no open legs (implied margin rate unknown)"
	}
	if sumNotional <= 0 {
		return true, false, fmt.Sprintf("all %d open legs skipped（报价缺无分母，隐含率不可判）", skipped)
	}
	used := equity / bufferMultiple // IMR 未知，以 MMR 为隐含率锚（保守同原口径）
	avail := equity - used
	if liveAvail != nil {
		avail = *liveAvail // live 实采可用优先（venue 权威口径）
	}
	return marginFloorJudgeSkipped(avail, used, sumNotional, addNotionalUSD, skipped)
}

// marginOpenNotional venue OPEN 腿名义 Σ（隐含率分母）。D-565 S6：legCount=匹配腿数
// （有腿与否），skipped=报价缺被跳腿数——全跳腿时不得混同「无腿」静默（known=false
// 透出跳腿计数；跳腿→分母偏小→隐含率偏大保守向，但禁静默）。
func (s *Service) marginOpenNotional(ctx context.Context, venue, vt string) (sum float64, legCount, skipped int, err error) {
	legs, err := s.store.ListOpenLegsForMargin(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, l := range legs {
		lv, lt := legSnapshotVenue(l.Venue)
		if lv != venue || lt != vt {
			continue
		}
		legCount++
		if n, nok := marginLegNotionalUSD(s.qbus, s.legContractSizeOf, l); nok {
			sum += n
		} else {
			skipped++
		}
	}
	return sum, legCount, skipped, nil
}

// latestVenueSnapshotSafe 单 venue 账户行查询（#1c：实时态=AccountMonitor；
// nil acctMon/store 安全——部分构造 Service → ok=false 如实「未采」）。
func (s *Service) latestVenueSnapshotSafe(ctx context.Context, venue, vt string) (*pgstore.VenueAccountLatest, bool, error) {
	return s.liveAccountLatestFor(ctx, venue, vt)
}

// marginFloorJudgeSkipped D-565 S6：判定明细附跳腿计数（报价缺腿不进分母=隐含率偏大保守向）。
func marginFloorJudgeSkipped(avail, used, sumNotional, addNotionalUSD float64, skipped int) (bool, bool, string) {
	ok, known, reason := marginFloorJudge(avail, used, sumNotional, addNotionalUSD)
	if skipped > 0 {
		reason += fmt.Sprintf("（报价缺跳过 %d 腿，分母偏小=隐含率偏大保守向）", skipped)
	}
	return ok, known, reason
}

func marginFloorJudge(avail, used, sumNotional, addNotionalUSD float64) (bool, bool, string) {
	if used <= 0 {
		return true, false, "used margin <=0 (implied rate unknown)"
	}
	required := addNotionalUSD * used / sumNotional
	if avail < required {
		return false, true, fmt.Sprintf("avail $%.2f < required $%.2f（隐含率 %.1f%% × 新仓 $%.2f）",
			avail, required, used/sumNotional*100, addNotionalUSD)
	}
	return true, true, fmt.Sprintf("avail $%.2f >= required $%.2f", avail, required)
}

// VenueCommissionBps D-406 S3：venue 单腿 taker 佣金率（commissionResolver 透出；
// nil resolver → 0，调用方须走常量表回退禁静默零值）。
func (s *Service) VenueCommissionBps(venueKey string) float64 {
	if s.commissionFor == nil {
		return 0
	}
	return s.commissionFor(venueKey)
}
