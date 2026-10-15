// paper_delivery.go 日历基差 P0 纸面轨（docs/日历基差提案模板设计.md 审后稿）：
// 配对=binance 同所同 canonical 跨 contract_type（perp_delivery——交割空腿×现货多腿）；
// PnL=Δbasis 市值重估（(entry_bps−cur_bps)×qty/1e4 绝对写，非 funding 差×天数）；
// 退出=到期 T−1（expiry−24h）+ data_lost（连缺 3 轮）；USD_UM/跨所不进 P0。
package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"nox/internal/data/fact"
	"nox/internal/research/scan"
	"nox/internal/store/pgstore"
)

const (
	// paperDeliveryMaxActive perp_delivery 活跃行上限（P0 面=binance USDT 季度 4 符号，
	// 8 给后续月度列预留——各轨不互挤同 perp 轨）。
	paperDeliveryMaxActive = 8
	// deliveryMissExitRounds 交割行连缺轮数→data_lost（同 perp 轨口径 3）。
	deliveryMissExitRounds = 3
	// deliveryExitBeforeExpiry 到期前退出窗 T−1（24h——不留到 venue 自动结算，设计 §4）。
	deliveryExitBeforeExpiry = 24 * time.Hour
)

// deliverySpreadSource 交割腿盘口价差源（滑点前置；binance 实现=bookTicker REST）。
type deliverySpreadSource interface {
	Venue() string
	DeliverySpreadBps(ctx context.Context, sym string) (float64, error)
}

// calPair 日历基差候选对（同 venue+canonical，交割行 basis_ann>0 才有 carry）。
type calPair struct {
	Canon     string
	FutSym    string
	Expiry    time.Time
	BasisAnn  float64
	BasisFrac float64 // mark/index−1（小数）
	SpreadBps *float64
}

// deliveryPairs P0 配对：binance 交割行 ∧ 同 canonical 有 binance PERP 行（方向=正基差
// contango 空交割×多现货；basis≤0 backwardation P0 不收——设计 §1）。basis_ann 缺=不开仓。
func deliveryPairs(rows []normRow, spreadBySym map[string]*float64, now time.Time) []calPair {
	perpCanon := make(map[string]bool)
	for _, n := range rows {
		if n.Venue == "binance" && n.ContractType == ctPerp {
			perpCanon[n.Canonical] = true
		}
	}
	var out []calPair
	for _, n := range rows {
		if n.Venue != "binance" || n.ContractType != ctFutures || !perpCanon[n.Canonical] {
			continue
		}
		ba := basisAnn(n.Mark, n.Index, n.ExpiryTS, now)
		if ba == nil || *ba <= 0 {
			continue // 基差缺/非正=不开仓不编（禁线）
		}
		out = append(out, calPair{
			Canon: n.Canonical, FutSym: n.Symbol, Expiry: n.ExpiryTS,
			BasisAnn: *ba, BasisFrac: n.Mark/n.Index - 1,
			SpreadBps: spreadBySym[n.Symbol],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BasisAnn > out[j].BasisAnn })
	return out
}

// stepPaperDelivery 纸面步进：①滑点前置 facts+开仓 ②Δbasis 市值重估 ③T−1/缺行退出。
func stepPaperDelivery(ctx context.Context, st *pgstore.Store, srcs []Source, rows []normRow, now time.Time) (opened []string, exited int, err error) {
	if st == nil {
		return nil, 0, nil
	}
	// 滑点前置：binance bookTicker 逐交割符号读 → facts（delivery_depth_spread_bps）。
	// 读败→该符号 map 缺→行标「滑点未知」诚实降级不判死。
	var spreadSrc deliverySpreadSource
	for _, s := range srcs {
		if ds, ok := s.(deliverySpreadSource); ok && s.Venue() == "binance" {
			spreadSrc = ds
		}
	}
	spreadBySym := map[string]*float64{}
	var facts []fact.Fact
	for _, n := range rows {
		if n.Venue != "binance" || n.ContractType != ctFutures {
			continue
		}
		if spreadSrc != nil {
			if sp, serr := spreadSrc.DeliverySpreadBps(ctx, n.Symbol); serr == nil {
				spreadBySym[n.Symbol] = &sp
				facts = append(facts, fact.Fact{Kind: "delivery_depth_spread_bps", Venue: "binance",
					Symbol: n.Symbol, Value: sp, Unit: fact.UnitBps, Ts: now, Src: "universe-radar"})
			} else {
				log.Printf("[universe-radar] bookTicker %s 读败（滑点未知）: %v", n.Symbol, serr)
			}
		}
	}
	if len(facts) > 0 {
		if ferr := st.InsertFacts(ctx, facts); ferr != nil {
			log.Printf("[universe-radar] 交割价差 facts 落库败: %v", ferr)
		}
	}

	active, err := st.ListActivePaperSpotCarry(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("delivery paper step: list active: %w", err)
	}
	accrable, err := st.ListAccrablePaperSpotCarry(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("delivery paper step: list accrable: %w", err)
	}
	activeByKey := make(map[string]struct{}, len(active))
	delActive := 0
	for _, p := range active {
		activeByKey[p.VenueA+"|"+p.VenueB+"|"+p.Symbol] = struct{}{}
		if p.Structure == "perp_delivery" {
			delActive++
		}
	}
	// ① 开仓（basis_ann 降序防 cap 饿死）
	for _, c := range deliveryPairs(rows, spreadBySym, now) {
		key := "binance|binance|" + c.FutSym
		if _, ok := activeByKey[key]; ok || delActive+len(opened) >= paperDeliveryMaxActive {
			continue
		}
		bps := c.BasisFrac * 1e4
		r := &pgstore.PaperSpotCarry{
			VenueA: "binance", VenueB: "binance",
			VenueTypeA: "futures", VenueTypeB: "spot",
			Symbol: c.FutSym, Side: "perp_delivery", Structure: "perp_delivery",
			EntryFundingAnn: c.BasisAnn, EntryBasisBps: &bps, LastBasisBps: &bps,
			QtyUSD:             scan.PaperQtyUSD,
			EstRoundTripFeeUSD: scan.PaperSpotPerpFeeUSD(ctx, st, "binance", "binance", scan.PaperQtyUSD),
		}
		if c.SpreadBps == nil {
			r.Note = "滑点未知"
		}
		if err := st.InsertPaperSpotCarry(ctx, r); err != nil {
			if err == pgstore.ErrPaperExists {
				continue
			}
			return opened, exited, fmt.Errorf("delivery paper step: open %s: %w", c.FutSym, err)
		}
		opened = append(opened, c.FutSym)
	}
	// ②③ 重估+退出：Δbasis 市值重估绝对写（accrueTicks 每轮+1 观测收敛路径）。
	futBySym := map[string]normRow{}
	for _, n := range rows {
		if n.ContractType == ctFutures {
			futBySym[n.Venue+"|"+n.Symbol] = n
		}
	}
	var dataLost []string // D-535①：data_lost 聚合 warn（一轮至多一条）
	for _, p := range accrable {
		if p.Structure != "perp_delivery" {
			continue
		}
		f, ok := futBySym[p.VenueA+"|"+p.Symbol]
		curOK := ok && f.Mark > 0 && f.Index > 0
		if !curOK {
			miss := p.MissStreak + 1
			if p.Status == "active" && miss >= deliveryMissExitRounds {
				if err := st.ExitPaperSpotCarry(ctx, p.ID, "data_lost", nil); err != nil {
					return opened, exited, fmt.Errorf("delivery paper step: exit miss %d: %w", p.ID, err)
				}
				exited++
				dataLost = append(dataLost, p.VenuePair()+"|"+p.Symbol)
				continue
			}
			lastAnn := 0.0
			if p.LastFundingAnn != nil {
				lastAnn = *p.LastFundingAnn
			}
			if err := st.AccruePaperSpotCarry(ctx, p.ID, lastAnn, 0, p.LowStreak, miss, nil, nil, false); err != nil {
				return opened, exited, fmt.Errorf("delivery paper step: miss %d: %w", p.ID, err)
			}
			continue
		}
		if p.EntryBasisBps == nil {
			log.Printf("[universe-radar] perp_delivery %d 入场基差缺（无法重估，待修数）", p.ID)
			continue
		}
		curBps := (f.Mark/f.Index - 1) * 1e4
		mtm := (*p.EntryBasisBps - curBps) * p.QtyUSD / 1e4 // 空交割×多现货：基差收敛=赚
		curAnn := basisAnn(f.Mark, f.Index, f.ExpiryTS, now)
		lastAnn := 0.0
		if curAnn != nil {
			lastAnn = *curAnn
		}
		// T−1：到期前 24h 退（到期清算尖峰不留给 venue 自动结算）
		if p.Status == "active" && !f.ExpiryTS.IsZero() && f.ExpiryTS.Sub(now) < deliveryExitBeforeExpiry {
			if err := st.ExitPaperSpotCarry(ctx, p.ID, "expiry_t_minus_1", &mtm); err != nil {
				return opened, exited, fmt.Errorf("delivery paper step: exit t-1 %d: %w", p.ID, err)
			}
			exited++
			continue
		}
		if err := st.AccruePaperSpotCarry(ctx, p.ID, lastAnn, mtm, p.LowStreak, 0, &curBps, &now, true); err != nil {
			return opened, exited, fmt.Errorf("delivery paper step: accrue %d: %w", p.ID, err)
		}
	}
	scan.WarnDataLostExits(ctx, st, "perp_delivery", dataLost) // D-535①
	return opened, exited, nil
}

// paperDeliveryRound main 装配壳：步进+开仓 info 告警+轮日志。
func paperDeliveryRound(ctx context.Context, st *pgstore.Store, srcs []Source, rows []normRow, now time.Time) {
	opened, exited, err := stepPaperDelivery(ctx, st, srcs, rows, now)
	if err != nil {
		log.Printf("universe-radar: paper delivery step: %v", err)
		return
	}
	if len(opened) > 0 {
		if aerr := st.InsertAlert(ctx, nil, "info", "[日历基差纸面开仓] "+strings.Join(opened, ", ")); aerr != nil {
			log.Printf("universe-radar: paper delivery alert: %v", aerr)
		}
	}
	if len(opened) > 0 || exited > 0 {
		log.Printf("[universe-radar] paper delivery: opened=%d exited=%d", len(opened), exited)
	}
}
