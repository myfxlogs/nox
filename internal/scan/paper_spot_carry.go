// paper_spot_carry.go D-350 阶段②：spot-carry 纸面验证轨步进（开仓/累积/退出，零执行风险）。
// 记账口径：accrued += sideFactor×last_funding_ann/100/365×间隔天×qty（雷达快照费率，
// 诚实声明非逐结算值）；short_perp 收正 funding（sideFactor=+1），long_perp 收负 funding
// （sideFactor=−1，funding 为负 → 收入为正）。
package scan

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"arb/internal/store/pgstore"
)

// 纸面轨参数（D-350 阶段②定值 + D-350R1 S4 闸校准 v1.1：band300+vol$10M+cap12+floor20——
// 校准依据=生产 fin_cex_universe 实查：net≥20 单闸放 221 行含死流动性长尾；band[20,300]+
// vol24h≥$10M=27 行，GUSDT/B2USDT/BRUSDT 全在）。
const (
	// PaperOpenNetPct 纸面开仓下限：net_spot_carry ≥ 20%/yr。
	PaperOpenNetPct = 20.0
	// PaperMaxNetPct 开仓上限（band 上限：≥300% 为死流动性长尾区，出 band 不开）。
	PaperMaxNetPct = 300.0
	// PaperMinVol24hUSD 流动性闸：24h 成交额下限（nil 亦不开——可交易性第一性判别）。
	PaperMinVol24hUSD = 1e7
	// PaperMaxActive 活跃纸面行硬上限（候选按 net 降序开到上限止）。
	PaperMaxActive = 12
	// PaperQtyUSD 虚拟仓位（固定 $1000，不复利）。
	PaperQtyUSD = 1000.0
	// paperExitNormalizedPct 退出线：|last_funding_ann| < 10%/yr。
	paperExitNormalizedPct = 10.0
	// paperExitStreakRounds 连续低于退出线的轮数。
	paperExitStreakRounds = 2
	// paperMaxAgeDays 满龄强评（天）。
	paperMaxAgeDays = 7.0
)

// PaperStore D-350 阶段②：纸面轨存储依赖（*pgstore.Store 原生实现）。
type PaperStore interface {
	ListCexUniverse(ctx context.Context) (map[string]pgstore.CexUniverseRow, error)
	ListActivePaperSpotCarry(ctx context.Context) ([]pgstore.PaperSpotCarry, error)
	ListAccrablePaperSpotCarry(ctx context.Context) ([]pgstore.PaperSpotCarry, error)
	InsertPaperSpotCarry(ctx context.Context, r *pgstore.PaperSpotCarry) error
	AccruePaperSpotCarry(ctx context.Context, id int64, lastFundingAnn, accruedAdd float64, lowStreak int) error
	ExitPaperSpotCarry(ctx context.Context, id int64, reason string) error
}

// StepPaperSpotCarry 每轮扫描后调用：①开仓（20≤net≤300 ∧ vol24h≥$10M ∧ 持续≥48h ∧ active<12，
// 候选按 net 降序开到上限止）②逐仓累积（active+demo，R1 S5）③退出（|funding|<10% 连续 2 轮 →
// normalized；满 7 天 → age_7d 强评——**仅 active**，demo 行只累积不自动退=人工/业主域）。
// 返回本轮开仓 symbol 清单（wire 侧聚一条开仓事件告警，D-350R1 S3）与退出行数。
func StepPaperSpotCarry(ctx context.Context, st PaperStore, now time.Time) (opened []string, exited int, err error) {
	uni, err := st.ListCexUniverse(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("paper step: list cex universe: %w", err)
	}
	active, err := st.ListActivePaperSpotCarry(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("paper step: list active: %w", err)
	}
	// 累积清单在开仓前加载：本轮新开行次轮起算（开仓时 last_accrue_ts=DB now()，
	// 若后加载会立即吃掉一段「参数钟−DB 钟」偏移——R1 实测 2h 偏差根因）。
	accrable, err := st.ListAccrablePaperSpotCarry(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("paper step: list accrable: %w", err)
	}
	activeByKey := make(map[string]struct{}, len(active))
	for _, p := range active {
		activeByKey[p.Venue+"|"+p.Symbol] = struct{}{}
	}
	// ①开仓（band+vol+cap v1.1；候选按 net 降序）
	type cand struct {
		key     string
		u       pgstore.CexUniverseRow
		funding float64
	}
	cands := []cand{}
	for key, u := range uni {
		if u.Status != "TRADING" || (u.Tier != "T1" && u.Tier != "T3") || u.FundingAnn == nil {
			continue
		}
		if now.Sub(u.FirstSeen) < spotCarryMinAge {
			continue
		}
		net := abs(*u.FundingAnn) - SPOT_CARRY_COST_PCT
		if net < PaperOpenNetPct || net > PaperMaxNetPct {
			continue
		}
		if u.Vol24h == nil || *u.Vol24h < PaperMinVol24hUSD {
			continue
		}
		if _, ok := activeByKey[key]; ok {
			continue
		}
		cands = append(cands, cand{key: key, u: u, funding: *u.FundingAnn})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].funding > cands[j].funding })
	for _, c := range cands {
		if len(activeByKey)+len(opened) >= PaperMaxActive {
			break
		}
		side := "short_perp"
		if c.funding < 0 {
			side = "long_perp"
		}
		r := &pgstore.PaperSpotCarry{
			Venue: c.u.Venue, Symbol: c.u.Symbol, Side: side,
			EntryFundingAnn: c.funding, QtyUSD: PaperQtyUSD,
		}
		if err := st.InsertPaperSpotCarry(ctx, r); err != nil {
			if err == pgstore.ErrPaperExists {
				continue
			}
			return opened, exited, fmt.Errorf("paper step: open %s: %w", c.key, err)
		}
		opened = append(opened, c.u.Symbol)
	}
	// ②累积（active+demo）+ ③退出（仅 active）
	for _, p := range accrable {
		u, ok := uni[p.Venue+"|"+p.Symbol]
		if !ok || u.FundingAnn == nil {
			continue // 雷达本轮缺行（停更/摘牌瞬窗）→ 跳过不误退
		}
		f := *u.FundingAnn
		base := p.EntryTs
		if p.LastAccrueTs != nil {
			base = *p.LastAccrueTs
		}
		days := now.Sub(base).Hours() / 24
		if days < 0 {
			days = 0
		}
		sideFactor := 1.0
		if p.Side == "long_perp" {
			sideFactor = -1.0
		}
		accruedAdd := sideFactor * f / 100 / 365 * days * p.QtyUSD
		streak := p.LowStreak
		if math.Abs(f) < paperExitNormalizedPct {
			streak++
		} else {
			streak = 0
		}
		if p.Status != "active" {
			// demo 行只累积不自动退（出场=人工/业主域，R1 S5）
			if err := st.AccruePaperSpotCarry(ctx, p.ID, f, accruedAdd, p.LowStreak); err != nil {
				return opened, exited, fmt.Errorf("paper step: accrue demo %d: %w", p.ID, err)
			}
			continue
		}
		if streak >= paperExitStreakRounds {
			if err := st.ExitPaperSpotCarry(ctx, p.ID, "normalized"); err != nil {
				return opened, exited, fmt.Errorf("paper step: exit %d: %w", p.ID, err)
			}
			exited++
			continue
		}
		if now.Sub(p.EntryTs).Hours()/24 >= paperMaxAgeDays {
			if err := st.ExitPaperSpotCarry(ctx, p.ID, "age_7d"); err != nil {
				return opened, exited, fmt.Errorf("paper step: exit age %d: %w", p.ID, err)
			}
			exited++
			continue
		}
		if err := st.AccruePaperSpotCarry(ctx, p.ID, f, accruedAdd, streak); err != nil {
			return opened, exited, fmt.Errorf("paper step: accrue %d: %w", p.ID, err)
		}
	}
	return opened, exited, nil
}
