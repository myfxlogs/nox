// service_capital_coverage.go —— D-359 S2：资金不眠覆盖率 RPC（口径 SSOT=D-356 §1.1）。
// digest 段同源双实现：复用服务层既有查询（TrxEnergyRentalAPR 直调零复刻）；行序/状态文本/
// 汇总语义与 digest capital_coverage*.go 对齐，测试锚数值同源防漂移。
// TRX 余额口径=balance+tron_power（D-356-R1）；venue 现金=闲置（睡钱）；空源行省略不炸 RPC。
package dashboard

import (
	"context"
	"fmt"
	"time"

	"arb/internal/capitalview"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/keydomain"

	"connectrpc.com/connect"
)

// capitalCoverageCaliberNote 口径注记（与 digest renderCapitalCoverageSection 注记同文本）。
const capitalCoverageCaliberNote = "口径：快照时点在产=TRX 在赚+保证金占用+CMC 持仓中（非时序加权，日级趋势看历史行）；保证金占用=Σ(equity−free_margin) 账户级（MT5 优先帧直带 Margin），含同账户手动仓、腿级切分不可得（诚实上限）；账户可用额未采→该账户占用不计（禁 eq−0 假满占，覆盖率如实低估）；总资金=Σ账户净值+TRX+CMC（D-127 业主令：杠杆名义额退出合计，仅作占用行 note 参考值）；加权年化分母=有年化行余额（占用行按占用额加权；CMC 手动仓/未采/纸面/venue 现金不进）；仅实盘账户/持仓入表（demo/sim/纸面=噪声不入）；venue 账户现金=闲置不计在产（睡钱暴露；CEX=USDT 标余额，USDT/LDUSDT 计、杂币不折算；MT=USD 同视）；CFD 周末闭市持仓中≠空转；占用行年化=position_daily_income 近7日日均息差（TRX 收益×快照价+利息，浮动盈亏不计）按名义额折算；能量租赁年化=D-258 30d 口径。"

// venueTypeCn D-125：venue 类型域词中文化（循前端 VenueTag 惯例：spot=现货/perp=永续/
// futures=交割；funding=资金钱包/unified=统一账户/earn=理财；MT 账户=MT4/MT5；
// 未知类型原样透出不猜）。
func venueTypeCn(vt string) string {
	switch vt {
	case "spot":
		return "现货"
	case "perp":
		return "永续"
	case "futures":
		return "交割"
	case "funding":
		return "资金钱包"
	case "unified":
		return "统一账户"
	case "earn":
		return "理财"
	case "mt4":
		return "MT4"
	case "mt5":
		return "MT5"
	default:
		return vt
	}
}

// srcCn D-125：资金读口来源域词中文化（值域=capitalview 常量 ws/rest/mt_frame/snap）。
func srcCn(src string) string {
	switch src {
	case "ws":
		return "私有流"
	case "rest":
		return "实拉校准"
	case "mt_frame":
		return "MT5 流帧"
	case "snap":
		return "快照"
	default:
		return src
	}
}

// GetCapitalCoverage D-359：五源逐池行+汇总（后端算，前端零计算）。
func (s *Service) GetCapitalCoverage(ctx context.Context, req *connect.Request[v1.GetCapitalCoverageRequest]) (*connect.Response[v1.GetCapitalCoverageResponse], error) {
	rows := buildCapitalCoverageRows(ctx, s)
	resp := summarizeCapitalCoverage(rows)
	resp.CaliberNote = capitalCoverageCaliberNote
	return connect.NewResponse(resp), nil
}

// buildCapitalCoverageRows 五源逐池（任一源失败/空 → 该行省略；全缺 → 空切片=前端「数据源全缺」）。
// D-127 行序：TRX→CMC→保证金占用→venue 账户（占用行恒在 venue 行前，读感=钱用在哪在前）。
func buildCapitalCoverageRows(ctx context.Context, s *Service) []*v1.CapitalCoverageRow {
	var rows []*v1.CapitalCoverageRow
	if r := trxPoolRow(ctx, s); r != nil {
		rows = append(rows, r)
	}
	if r := cmcPoolRow(ctx, s); r != nil {
		rows = append(rows, r)
	}
	venueRows, occ := venueAccountRows(ctx, s)
	rows = append(rows, marginOccupancyRow(ctx, s, occ))
	rows = append(rows, venueRows...)
	return rows
}

// row 构造辅助（指针字段快捷）。
func covRow(pool string) *v1.CapitalCoverageRow {
	return &v1.CapitalCoverageRow{Pool: pool}
}

// trxPoolRow ①TRX：余额=(balance_sun+tron_power)/1e6×USD 价（D-356-R1 口径锚）；
// 年化=LastTRXVoteApr.CurrentRate+TrxEnergyRentalAPR(30)（D-258 口径复用零复刻）；
// 价三级 fallback=fin_cmc_prices(X-AFQUL)→ticks TRXUSDT mid→无价「—」+数量入 note。
func trxPoolRow(ctx context.Context, s *Service) *v1.CapitalCoverageRow {
	acct, err := s.store.LastTRXAccount(ctx)
	if err != nil {
		return nil
	}
	r := covRow("TRX 质押+投票+能量")
	r.Status = "在赚"
	r.Productive = true
	r.RealMoney = true
	voteAPR, hasVote := 0.0, false
	if va, err := s.store.LastTRXVoteApr(ctx); err == nil {
		voteAPR, hasVote = va.CurrentRate, true
		r.AprPct = voteAPR
		r.HasApr = true
	}
	energyAPR, sampleDays, err := s.TrxEnergyRentalAPR(ctx, 30)
	// D-258 语义：样本<30d/零在押 → aprPct=0 不猜；sampleDays>=30 即有效读数
	energyOK := err == nil && sampleDays >= 30
	note := ""
	if hasVote {
		note = fmt.Sprintf("投票 %.2f%%", voteAPR)
	}
	if energyOK {
		note += fmt.Sprintf("+能量租赁 %.2f%%", energyAPR)
		if hasVote {
			r.AprPct += energyAPR
		} else {
			r.AprPct = energyAPR
			r.HasApr = true
		}
	}
	balTRX := float64(acct.BalanceSun+acct.TronPower) / 1e6
	if price, ok := trxUSDPrice(ctx, s); ok {
		r.BalanceUsd = balTRX * price
		r.HasBalance = true
		r.Note = fmt.Sprintf("%s；余额=可用 %.0f+投票权 %.0f TRX", note,
			float64(acct.BalanceSun)/1e6, float64(acct.TronPower)/1e6)
	} else {
		r.Note = fmt.Sprintf("%s；TRX 数量 %.0f（可用 %.0f+投票权 %.0f，无 USD 价，不猜）", note,
			balTRX, float64(acct.BalanceSun)/1e6, float64(acct.TronPower)/1e6)
	}
	return r
}

// trxUSDPrice TRX USD 价三级 fallback（digest trxUSDPrice 同序）。
func trxUSDPrice(ctx context.Context, s *Service) (float64, bool) {
	if mid, _, err := s.store.LastCMCPriceMidWithTime(ctx, "X-AFQUL"); err == nil && mid > 0 {
		return mid, true
	}
	return s.store.LatestTickMid(ctx, "binance", "TRXUSDT") // D-359：TRXUSD 价锚=binance 现货腿报价
}

// cmcPoolRow ②CMC：余额=LastCMCAccountSummary.TotalAccountValue；
// 最新持仓快照有行→「手动持仓中（浮盈 $X）」/无行→「现金闲置」；手动域无 carry 不入加权。
func cmcPoolRow(ctx context.Context, s *Service) *v1.CapitalCoverageRow {
	sum, err := s.store.LastCMCAccountSummary(ctx)
	if err != nil {
		return nil
	}
	r := covRow("CMC 账户（手动持仓监控域）")
	r.BalanceUsd = sum.TotalAccountValue
	r.HasBalance = true
	r.RealMoney = true
	if pnl, n, err := s.store.LastCMCPositionSnapshotSummary(ctx); err == nil && n > 0 && pnl != nil {
		r.Status = fmt.Sprintf("手动持仓中（浮盈 $%.2f）", *pnl)
		r.Productive = true
	} else {
		r.Status = "现金闲置"
	}
	return r
}

// venueOcc D-127：账户占用聚合（占用=venue 净值中真被保证金占用的子集，USDT/USD 同视账户口径）。
type venueOcc struct {
	sum     float64 // Σ已知占用
	known   int     // 占用已知账户数
	missing int     // 可用额未采账户数（fail-closed 不入分子）
}

// accountOccupied D-127 新口径①：单账户占用=equity−free_margin；MT 帧直带 Margin>0 优先
// （帧值即 venue 原生占用）；fm≤0 视为未采（live 视图零值与快照 NULL 同型——禁 eq−0 假满占）；
// eq<fm 数据异常同样 fail-closed 不猜。资金面未出（ok=false）→ 未采。
func accountOccupied(f capitalview.Funds, ok bool) (float64, bool) {
	if !ok {
		return 0, false
	}
	if f.Margin > 0 {
		return f.Margin, true
	}
	if f.Available > 0 {
		occ := f.Equity - f.Available
		if occ < 0 {
			return 0, false
		}
		return occ, true
	}
	return 0, false
}

// marginOccupancyRow ③保证金占用（D-127 新口径，替代原「系统持仓」名义额行）：
// 值=Σ账户占用；名义额退出合计、降格 note 参考值；RealMoney=false=venue 净值子集视图
// （净值行已入分母，占用行不重复计）∧Productive=true 入在产分子；
// 年化仍按名义额折算（息差随名义产生），加权按占用额。
func marginOccupancyRow(ctx context.Context, s *Service, occ venueOcc) *v1.CapitalCoverageRow {
	r := covRow("保证金占用")
	r.Productive = true
	r.RealMoney = false
	var notional float64
	if ens, err := s.store.ActiveNotionalByEnv(ctx); err == nil {
		for _, e := range ens {
			notional += e.NotionalUSD // D-360 S4：查询已滤仅 live
		}
	}
	avg, hasAvg, err := s.store.AvgDailyIncomeCarry7d(ctx)
	if err != nil {
		hasAvg = false
	}
	if occ.known > 0 {
		r.BalanceUsd = occ.sum
		r.HasBalance = true
		r.Status = "占用中"
	} else if notional > 0 {
		r.Status = "占用未知" // 有仓但可用额全未采——fail-closed 不入分子（防虚胖）
	} else {
		r.Status = "零占用"
	}
	note := "账户级占用（含同账户手动仓，腿级切分不可得）"
	if occ.missing > 0 {
		note += fmt.Sprintf("；%d 账户可用额未采不计", occ.missing)
	}
	if notional > 0 {
		note += fmt.Sprintf("；名义额参考 $%.2f（不入合计）", notional)
		if hasAvg {
			r.AprPct = avg * 365 / notional * 100
			r.HasApr = true
		} else {
			r.AprText = "新开仓"
		}
	} else if occ.known == 0 {
		note = "无活跃仓·无账户占用"
	}
	r.Note = note
	return r
}

// venueAccountRows ⑤CEX/MT：每 venue 最新快照（D-358 落库）；USDT/USD 行同视 USD 入总资金分母，
// 非 USDT 不折算标原币（D-358 §4）；现金=闲置 Productive=false。D-127：行内附占用/可用注记
// 并聚合占用（USDT/USD 同视账户口径）。空表/缺表 → 行省略（D-359 §S2 口径：rows 全空=
// 前端「数据源全缺」；与 digest 段的未采回退行不同——双实现各按其 SSOT 条款）。
func venueAccountRows(ctx context.Context, s *Service) ([]*v1.CapitalCoverageRow, venueOcc) {
	lates, err := s.liveAccountLatest(ctx) // #1c：实时态（快照表=历史记录）
	if err != nil || len(lates) == 0 {
		return nil, venueOcc{}
	}
	cv := s.capitalView()
	out := make([]*v1.CapitalCoverageRow, 0, len(lates))
	var occ venueOcc
	for _, l := range lates {
		// D-125：池标签去「venue账户」前缀——venue 名+类型中文化已足（`okx·统一账户` 式）。
		r := covRow(l.Venue + "·" + venueTypeCn(l.VenueType))
		r.Status = "有数"
		// D-549：资金值走统一读口（CEX 活账本/REST 实拉）；快照行仅作枚举面+兜底值。
		f, ok := cv.LiveSnaps(ctx, keydomain.Join(l.Venue, l.VenueType), lates)
		eq, ccy, note := l.EquityRaw, l.Currency, ""
		if ok {
			eq = &f.Equity
			// D-126：MT 流帧币种恒空（MT5 帧无 Currency 字段→Funds.Currency 如实空串），
			// 空则保留快照行币种（快照=MT 币种唯一权威源）——否则 MT 真金漏出分母、
			// 覆盖率被抬高；快照亦空→ccy 空→余额列「—」fail-closed 不变。
			if f.Currency != "" {
				ccy = f.Currency
			}
			if f.Live {
				note = fmt.Sprintf("%s 活账本·%s", venueTypeCn(l.VenueType), srcCn(f.Src))
			} else {
				note = f.Note // 快照兜底龄注记（禁静默）
			}
		}
		if note == "" { // 三径全空：快照龄如实标
			age := time.Since(l.FetchedAt)
			note = fmt.Sprintf("%s 快照 %dh 前", venueTypeCn(l.VenueType), int(age.Hours()))
			if age > 24*time.Hour {
				note += "（陈旧）"
			}
		}
		if eq != nil && (ccy == "USDT" || ccy == "USD") { // D-361 S1：USDT/USD 同视 1:1
			r.BalanceUsd = *eq
			r.HasBalance = true
			r.RealMoney = true
			// D-127：占用聚合+可用注记（⑤「可用 X」小字；未采 fail-closed 如实标）。
			if o, known := accountOccupied(f, ok); known {
				occ.sum += o
				occ.known++
				r.Note = note + fmt.Sprintf("，可用 %.2f", f.Available)
			} else {
				occ.missing++
				r.Note = note + "，可用未采"
			}
		} else if eq != nil {
			r.Note = fmt.Sprintf("%s，净值 %.2f %s（非 USDT/USD 不折算）", note, *eq, ccy)
		} else {
			r.Note = note + "，净值空"
		}
		out = append(out, r)
	}
	return out, occ
}

// summarizeCapitalCoverage 汇总行（与 digest 渲染层同语义；D-127 拆门）：
// 总资金=RealMoney∧有余额（占用行=净值子集视图 RealMoney=false 不入，防双重计入）；
// 在产=Productive∧有余额（占用行入分子）；覆盖率=在产/总资金；
// 加权年化=Σbal×apr/Σ有年化有余额 bal（占用行按占用额加权）；total=0→has_summary=false，
// 无年化→has_weighted=false。
func summarizeCapitalCoverage(rows []*v1.CapitalCoverageRow) *v1.GetCapitalCoverageResponse {
	resp := &v1.GetCapitalCoverageResponse{Rows: rows}
	var wSum, wBal float64
	for _, r := range rows {
		if !r.HasBalance {
			continue
		}
		if r.RealMoney {
			resp.TotalUsd += r.BalanceUsd
		}
		if r.Productive {
			resp.ProductiveUsd += r.BalanceUsd
		}
		if r.HasApr {
			wSum += r.BalanceUsd * r.AprPct
			wBal += r.BalanceUsd
		}
	}
	if resp.TotalUsd > 0 {
		resp.CoveragePct = resp.ProductiveUsd / resp.TotalUsd * 100
		resp.HasSummary = true
	}
	if wBal > 0 {
		resp.WeightedApr = wSum / wBal
		resp.HasWeighted = true
	}
	return resp
}
