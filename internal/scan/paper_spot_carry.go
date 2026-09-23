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
	AccruePaperSpotCarry(ctx context.Context, id int64, lastFundingAnn, accruedAdd float64, lowStreak int, lastBasisBps *float64) error
	// D-410 S2：退出冻结收敛捕获（nil→列 NULL 诚实缺）。
	ExitPaperSpotCarry(ctx context.Context, id int64, reason string, convergenceUSD *float64) error
	// D-410 S4：cfd 轨 monitor basis 面与保底槽数据源（*pgstore.Store 原生实现）。
	XAssetMonitorRowNear(ctx context.Context, pair string, ts time.Time, maxGap time.Duration) (entrySpreadBps float64, ok bool, err error)
	LatestXAssetEntryGate(ctx context.Context, pair string, maxAge time.Duration) (entryBps, noiseBps, harvestFloorBps float64, hasFloor, ok bool, err error)
	LoadXAssetAllowCombos(ctx context.Context, environment string) ([]pgstore.XAssetAllowRow, error)
	LatestPaperExitTs(ctx context.Context, venue, symbol string) (*time.Time, error)
	// D-410 S4：保底槽宇宙行缺席 warn 节流（禁 `_ =` 吞错，cashflow-write-error 同族条款）。
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
}

// StepPaperSpotCarry 每轮扫描后调用：①开仓（20≤net≤300 ∧ vol24h≥$10M ∧ 持续≥48h ∧
// 连续 PaperEntryStreakRounds 轮全条件达标（D-383C 进场持续闸，entryStreak 由 wire_scan
// 装配一次逐轮传入）∧ active<12，达标候选按 net 降序开到上限止，streak 未满者不参与
// cap 竞争防尖峰占坑）②逐仓累积（active+demo，R1 S5）③退出（|funding|<10% 连续 2 轮 →
// normalized；满 7 天 → age_7d 强评——**仅 active**，demo 行只累积不自动退=人工/业主域）。
// 返回本轮开仓 symbol 清单（wire 侧聚一条开仓事件告警，D-350R1 S3）与退出行数。
// cexFeeBps：D-410 S3 开仓记账往返费用率源（=cfg.XAssetCexFeeBps，同源同值禁第二口径）。
func StepPaperSpotCarry(ctx context.Context, st PaperStore, entryStreak *EntryStreak, now time.Time, cexFeeBps float64) (opened []string, exited int, err error) {
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
		net     float64
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
		cands = append(cands, cand{key: key, u: u, funding: *u.FundingAnn, net: net})
	}
	// D-383C 进场持续闸：连续 PaperEntryStreakRounds 轮全条件达标才放行——任一条件失败/
	// 候选从 uni 消失由 Step 清零（键=venue|symbol）；streak 未满的键不进开仓排序。
	passed := make([]string, 0, len(cands))
	for _, c := range cands {
		passed = append(passed, c.key)
	}
	eligible := entryStreak.Step(passed, PaperEntryStreakRounds)
	// D-350R2 S2：排序键=net 降序（|funding|−cost，与闸同键）。签名 funding 降序会把负 funding
	// （long_perp）恒排全部正侧之后——cap 命中时 long_perp 侧系统性饿死，且 age_7d 退出即重进
	// 使饿死可永久化（实证：ESPUSDT net 292.85 净序应榜首，签名序垫底进不了 cap）。
	sort.Slice(cands, func(i, j int) bool { return cands[i].net > cands[j].net })
	for _, c := range cands {
		if _, ok := eligible[c.key]; !ok {
			continue // streak 未满，不参与 cap 竞争（防尖峰占坑）
		}
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
			EntryBasisBps: c.u.LastBasisBps, LastBasisBps: c.u.LastBasisBps, // D-354 S2：开仓基差快照（NULL 传播）
			EstRoundTripFeeUSD: paperEstFeeUSD(c.u.Venue, PaperQtyUSD, cexFeeBps), // D-410 S3：开仓一次性记账（venue 未覆盖→NULL 拒填）
		}
		if err := st.InsertPaperSpotCarry(ctx, r); err != nil {
			if err == pgstore.ErrPaperExists {
				continue
			}
			return opened, exited, fmt.Errorf("paper step: open %s: %w", c.key, err)
		}
		opened = append(opened, c.u.Symbol)
		entryStreak.Drop(c.key) // 开仓成功即删键（已 active，D-383C）
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
		var lastBasis *float64
		if u, ok := uni[p.Venue+"|"+p.Symbol]; ok {
			lastBasis = u.LastBasisBps // D-354 S2：每轮随 uni 更新（map 既有加载，零新查询）
		}
		if p.Status != "active" {
			// demo 行只累积不自动退（出场=人工/业主域，R1 S5）
			if err := st.AccruePaperSpotCarry(ctx, p.ID, f, accruedAdd, p.LowStreak, lastBasis); err != nil {
				return opened, exited, fmt.Errorf("paper step: accrue demo %d: %w", p.ID, err)
			}
			continue
		}
		if streak >= paperExitStreakRounds || now.Sub(p.EntryTs).Hours()/24 >= paperMaxAgeDays {
			reason := "normalized"
			if streak < paperExitStreakRounds {
				reason = "age_7d"
			}
			// D-410 S3：退出冻结收敛捕获（本轮 last 缺→回退行内 last，双缺→NULL 诚实缺）。
			if err := st.ExitPaperSpotCarry(ctx, p.ID, reason, paperExitConvergence(p, lastBasis)); err != nil {
				return opened, exited, fmt.Errorf("paper step: exit %d: %w", p.ID, err)
			}
			exited++
			continue
		}
		if err := st.AccruePaperSpotCarry(ctx, p.ID, f, accruedAdd, streak, lastBasis); err != nil {
			return opened, exited, fmt.Errorf("paper step: accrue %d: %w", p.ID, err)
		}
	}
	return opened, exited, nil
}
