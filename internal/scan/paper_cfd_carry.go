// paper_cfd_carry.go D-374 B 面：cfd×perp 纸面验证轨步进（开仓/累积/退出，零执行风险）。
// 触发实证：fin_carry_universe 浮现 SOL long_cfd_short_perp 277842155×binance net=10.95
// （MT5 facts swap_long=0=真 swap-free）——cfd 结构此前零验证通道（spot 轨不收、universe 无回溯）。
// 数据源零新查询：wire_scan 4h carry 环的 res.Rows 内存直传——行在=信号在，行消失=universe
// 判定不再成立（与 spot 轨 skip-on-miss 相反，miss 计 streak 同轨退出）。
package scan

import (
	"context"
	"fmt"
	"sort"
	"time"

	"arb/internal/store/pgstore"
)

// cfd 纸面轨参数（§2B 定值：地板 8 独立于 spot 20——cfd carry 稳态低于 spot 票价；
// cap 6 与 spot 12 并列不互相挤占；上限/streak/满龄复用 spot 常量）。
const (
	// PaperCfdOpenNetPct cfd 纸面开仓下限：net ≥ 8%/yr（SOL 10.95 实证在下）。
	PaperCfdOpenNetPct = 8.0
	// PaperCfdMaxNetPct 开仓上限（band 上限同 spot 语义：≥300% 为长尾区）。
	PaperCfdMaxNetPct = 300.0
	// PaperCfdMaxActive 活跃 cfd 纸面行硬上限。
	PaperCfdMaxActive = 6
)

// cfdStructures fin_carry_universe 的 cfd×perp 结构集（进 cfd 纸面轨的行子集判定）。
var cfdStructures = map[string]bool{"long_cfd_short_perp": true, "short_cfd_long_perp": true}

// StepPaperCfdCarry 每轮 carry 扫描后调用（res.Rows 直传）：
// ①开仓：Structure∈cfd 集 ∧ note=”（D-345 suspect 行拒）∧ net∈[8,300] ∧ 无同键活跃行；
//
//	候选 net 降序开到 cap（防 cap 命时低净饿死，同 spot R2 教训）；venue=mt×cex 组合串
//	（部分唯一索引 (venue,symbol) 天然唯一，与 spot 行不撞）。
//
// ②累积：accrued += NetAnnual/100/365×days×qty——NetAnnual 已=funding−swap 净额，
//
//	无 sideFactor（与 spot 轨公式差异点）；last_funding_ann 存当轮 FundingAnn（展示用）。
//
// ③退出（仅 active）：行消失或 net<8 → low_streak++，连续 paperExitStreakRounds 轮 →
//
//	normalized；满 paperMaxAgeDays → age_7d。demo 行只累积不自动退；行缺席时 demo 跳过
//	不误记。entry/last basis 列 NULL 诚实缺（fin_carry_universe 无 basis 数据，不臆造）。
func StepPaperCfdCarry(ctx context.Context, st PaperStore, rows []UniverseRow, now time.Time) (opened []string, exited int, err error) {
	active, err := st.ListActivePaperSpotCarry(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("cfd paper step: list active: %w", err)
	}
	// 累积清单在开仓前加载（同 spot 轨 R1 教训：本轮新开行次轮起算）。
	accrable, err := st.ListAccrablePaperSpotCarry(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("cfd paper step: list accrable: %w", err)
	}
	activeByKey := make(map[string]struct{}, len(active))
	cfdActive := 0
	for _, p := range active {
		activeByKey[p.Venue+"|"+p.Symbol] = struct{}{}
		if cfdStructures[p.Structure] {
			cfdActive++
		}
	}
	// ① 开仓
	type cfdCand struct {
		venueCombo string
		r          UniverseRow
	}
	cands := []cfdCand{}
	for _, r := range rows {
		if !cfdStructures[r.Structure] || r.Note != "" {
			continue
		}
		if r.NetAnnual < PaperCfdOpenNetPct || r.NetAnnual > PaperCfdMaxNetPct {
			continue
		}
		venueCombo := r.MtVenue + "×" + r.CexVenue
		if _, ok := activeByKey[venueCombo+"|"+r.Canonical]; ok {
			continue
		}
		cands = append(cands, cfdCand{venueCombo: venueCombo, r: r})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].r.NetAnnual > cands[j].r.NetAnnual })
	for _, c := range cands {
		if cfdActive+len(opened) >= PaperCfdMaxActive {
			break
		}
		side := "short_perp"
		if c.r.Structure == "short_cfd_long_perp" {
			side = "long_perp"
		}
		mv := c.r.MtVenue
		r := &pgstore.PaperSpotCarry{
			Venue: c.venueCombo, Symbol: c.r.Canonical, Side: side,
			EntryFundingAnn: c.r.FundingAnn, QtyUSD: PaperQtyUSD,
			Structure: c.r.Structure, MtVenue: &mv,
		}
		if err := st.InsertPaperSpotCarry(ctx, r); err != nil {
			if err == pgstore.ErrPaperExists {
				continue
			}
			return opened, exited, fmt.Errorf("cfd paper step: open %s: %w", c.venueCombo, err)
		}
		opened = append(opened, c.r.Canonical)
	}
	// ② 累积 + ③ 退出（仅 cfd 行；spot 轨行不归本函数）
	rowByKey := make(map[string]UniverseRow, len(rows))
	for _, r := range rows {
		if cfdStructures[r.Structure] && r.Note == "" {
			rowByKey[r.MtVenue+"×"+r.CexVenue+"|"+r.Canonical] = r
		}
	}
	for _, p := range accrable {
		if !cfdStructures[p.Structure] {
			continue
		}
		r, ok := rowByKey[p.Venue+"|"+p.Symbol]
		if !ok {
			if p.Status != "active" {
				continue // demo 行缺席：无数据不误记
			}
			// active 行缺席=信号死：miss 计 streak 同轨，零累积（accruedAdd=0，时钟照走）
			if p.LowStreak+1 >= paperExitStreakRounds {
				if err := st.ExitPaperSpotCarry(ctx, p.ID, "normalized"); err != nil {
					return opened, exited, fmt.Errorf("cfd paper step: exit miss %d: %w", p.ID, err)
				}
				exited++
				continue
			}
			prev := 0.0
			if p.LastFundingAnn != nil {
				prev = *p.LastFundingAnn
			}
			if err := st.AccruePaperSpotCarry(ctx, p.ID, prev, 0, p.LowStreak+1, nil); err != nil {
				return opened, exited, fmt.Errorf("cfd paper step: miss %d: %w", p.ID, err)
			}
			continue
		}
		base := p.EntryTs
		if p.LastAccrueTs != nil {
			base = *p.LastAccrueTs
		}
		days := now.Sub(base).Hours() / 24
		if days < 0 {
			days = 0
		}
		accruedAdd := r.NetAnnual / 100 / 365 * days * p.QtyUSD
		streak := p.LowStreak
		if r.NetAnnual < PaperCfdOpenNetPct {
			streak++
		} else {
			streak = 0
		}
		if p.Status != "active" {
			// demo 行只累积不自动退（同 spot 轨）
			if err := st.AccruePaperSpotCarry(ctx, p.ID, r.FundingAnn, accruedAdd, p.LowStreak, nil); err != nil {
				return opened, exited, fmt.Errorf("cfd paper step: accrue demo %d: %w", p.ID, err)
			}
			continue
		}
		if streak >= paperExitStreakRounds {
			if err := st.ExitPaperSpotCarry(ctx, p.ID, "normalized"); err != nil {
				return opened, exited, fmt.Errorf("cfd paper step: exit %d: %w", p.ID, err)
			}
			exited++
			continue
		}
		if now.Sub(p.EntryTs).Hours()/24 >= paperMaxAgeDays {
			if err := st.ExitPaperSpotCarry(ctx, p.ID, "age_7d"); err != nil {
				return opened, exited, fmt.Errorf("cfd paper step: exit age %d: %w", p.ID, err)
			}
			exited++
			continue
		}
		if err := st.AccruePaperSpotCarry(ctx, p.ID, r.FundingAnn, accruedAdd, streak, nil); err != nil {
			return opened, exited, fmt.Errorf("cfd paper step: accrue %d: %w", p.ID, err)
		}
	}
	return opened, exited, nil
}
