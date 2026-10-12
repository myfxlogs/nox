// service_capital_coverage.go —— D-359 S2：资金不眠覆盖率 RPC（口径 SSOT=D-356 §1.1）。
// digest 段同源双实现：复用服务层既有查询（TrxEnergyRentalAPR 直调零复刻）；行序/状态文本/
// 汇总语义与 digest capital_coverage*.go 对齐，测试锚数值同源防漂移。
// TRX 余额口径=balance+tron_power（D-356-R1）；venue 现金=闲置（睡钱）；空源行省略不炸 RPC。
package dashboard

import (
	"context"
	"fmt"
	"time"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/keydomain"

	"connectrpc.com/connect"
)

// capitalCoverageCaliberNote 口径注记（与 digest renderCapitalCoverageSection 注记同文本）。
const capitalCoverageCaliberNote = "口径：快照时点在产=TRX 在赚+保证金占用+CMC 持仓中（非时序加权，日级趋势看历史行）；保证金占用=Σ(equity−free_margin) 账户级（MT5 优先帧直带 Margin），含同账户手动仓、腿级切分不可得（诚实上限）；账户可用额未采→该账户占用不计（禁 eq−0 假满占，覆盖率如实低估）；总资金=Σ账户净值+TRX+CMC（D-127 业主令：杠杆名义额退出合计，仅作占用行 note 参考值）；加权年化分母=有年化行余额（占用行按占用额加权；CMC 手动仓/未采/纸面/venue 现金不进）；仅实盘账户/持仓入表（demo/sim/纸面=噪声不入）；venue 账户现金=闲置不计在产（睡钱暴露；CEX=USDT 标余额，USDT/LDUSDT 计、杂币不折算；MT=USD 同视）；CFD 周末闭市持仓中≠空转；占用行年化=position_daily_income 近7日日均息差（TRX 收益×快照价+利息，浮动盈亏不计）按名义额折算；能量租赁年化=D-258 30d 口径。"

// venueTypeCn D-125 建、D-130 域2②改按 venue 原生中文词（业主令：面名随所）：
// binance=现货/合约（perp→合约；D-133 理财面摘除——earn 落通用「赚币」词保险）、
// okx=资金账户/交易账户/赚币（funding/unified/earn）、gate=现货/合约/交割/理财；
// MT 账户=MT4/MT5；未知组合回退通用映射，终兜底原样透出不猜。
func venueTypeCn(venue, vt string) string {
	switch venue {
	case "binance":
		switch vt {
		case "perp":
			return "合约"
		case "spot":
			return "现货"
		}
	case "okx":
		switch vt {
		case "funding":
			return "资金账户"
		case "unified":
			return "交易账户"
		case "earn":
			return "赚币"
		}
	case "gate":
		switch vt {
		case "perp":
			return "合约"
		case "earn":
			return "理财"
		case "futures":
			return "交割"
		case "spot":
			return "现货"
		}
	}
	switch vt {
	case "spot":
		return "现货"
	case "perp":
		return "合约"
	case "futures":
		return "交割"
	case "funding":
		return "资金账户"
	case "unified":
		return "交易账户"
	case "earn":
		return "赚币"
	case "mt4":
		return "MT4"
	case "mt5":
		return "MT5"
	default:
		return vt
	}
}

// balanceCcyFoldable D-130 域3：余额币种可折入合计（USDT≈USD 1:1 业主令；空=历史行 USD 口径）。
func balanceCcyFoldable(ccy string) bool {
	return ccy == "" || ccy == "USD" || ccy == "USDT"
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
		// D-130 域2①：binance 资金钱包面官方已废除并入现货——覆盖率行不再枚举；
		// D-133 域1：binance 理财面整体摘除（业主令：实有杂币「只统计 USDT」不可达）。
		// 两面配置行停用/删随部署迁移；代码侧双保险，digest 同规。
		if l.Venue == "binance" && (l.VenueType == "funding" || l.VenueType == "earn") {
			continue
		}
		// D-125：池标签去「venue账户」前缀；D-130 域2②面名随 venue 原生词（`okx·交易账户` 式）。
		r := covRow(l.Venue + "·" + venueTypeCn(l.Venue, l.VenueType))
		// D-549：资金值走统一读口（CEX 活账本/REST 实拉）；快照行仅作枚举面+兜底值。
		// D-130 域1：账户行只认精确 venue+type 键（LiveSnapsExact），回退链=腿池语义禁入。
		f, ok := cv.LiveSnapsExact(ctx, keydomain.Join(l.Venue, l.VenueType), lates)
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
				note = fmt.Sprintf("%s 活账本·%s", venueTypeCn(l.Venue, l.VenueType), srcCn(f.Src))
			} else {
				note = f.Note // 快照兜底龄注记（禁静默）
			}
		}
		if note == "" { // 三径全空：快照龄如实标
			age := time.Since(l.FetchedAt)
			note = fmt.Sprintf("%s 快照 %dh 前", venueTypeCn(l.Venue, l.VenueType), int(age.Hours()))
			if age > 24*time.Hour {
				note += "（陈旧）"
			}
		}
		if eq != nil && (ccy == "USDT" || ccy == "USD") { // D-361 S1：USDT/USD 同视 1:1
			r.BalanceUsd = *eq
			r.HasBalance = true
			r.RealMoney = true
			r.BalanceCcy = ccy
			// D-127：占用聚合+可用注记（⑤「可用 X」小字；未采 fail-closed 如实标）。
			// D-128：状态=业务词（闲置/部分占用/占用未知），「有数」黑话清除。
			if o, known := accountOccupied(f, ok); known {
				occ.sum += o
				occ.known++
				r.Note = note + fmt.Sprintf("，可用 %.2f", f.Available)
				if o > 0 {
					r.Status = "部分占用"
					occ.items = append(occ.items, fmt.Sprintf("%s $%.2f", r.Pool, o))
				} else {
					r.Status = "闲置"
				}
			} else {
				occ.missing++
				r.Note = note + "，可用未采"
				r.Status = "占用未知"
			}
		} else if eq != nil && ccy != "" {
			// D-130 域3：原币直显「流中啥显啥」（HasBalance=true 供展示；合计门 balanceCcyFoldable 拒纳）；
			// 币种未知（空）→ 无从原币显示，落未采 fail-closed。
			r.BalanceUsd = *eq
			r.HasBalance = true
			r.BalanceCcy = ccy
			r.Status = "未折算"
			r.Note = fmt.Sprintf("%s，净值 %.2f %s（非 USDT/USD 不折算）", note, *eq, ccy)
		} else {
			r.Status = "未采"
			r.Note = note + "，净值空"
		}
		out = append(out, r)
	}
	return out, occ
}

// summarizeCapitalCoverage 汇总行（与 digest 渲染层同语义；D-127 拆门；D-130 域3 加币种门）：
// 总资金=RealMoney∧有余额∧币种可折（USDT≈USD 1:1；原币展示行不入合计防混币相加）；
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
		if r.RealMoney && balanceCcyFoldable(r.BalanceCcy) {
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
