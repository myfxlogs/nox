// paper_cfd_carry.go D-374 B 面：cfd×perp 纸面验证轨步进（开仓/累积/退出，零执行风险）。
// 触发实证：fin_carry_universe 浮现 SOL long_cfd_short_perp 277842155×binance net=10.95
// （MT5 facts swap_long=0=真 swap-free）——cfd 结构此前零验证通道（spot 轨不收、universe 无回溯）。
// 数据源零新查询：wire_scan 4h carry 环的 res.Rows 内存直传——行在=信号在，行消失=universe
// 判定不再成立（与 spot 轨 skip-on-miss 相反，miss 计 streak 同轨退出）。
package scan

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
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
	// PaperCfdMaxActive 活跃 cfd 纸面行硬上限（保底槽不占此额，D-410 S4）。
	PaperCfdMaxActive = 6
)

// PaperCfdOpts D-410 S4 装配参数（wire_scan 注入 cfg/env 值，scan 层不读 env）。
type PaperCfdOpts struct {
	// CexFeeBps 往返费率源（=cfg.XAssetCexFeeBps 同源同值）。
	CexFeeBps float64
	// AuthorizedMaxActive 授权组合保底槽独立上限（env ARB_PAPER_AUTHORIZED_MAX_ACTIVE，默认 8）。
	AuthorizedMaxActive int
	// ReopenCooldown 保底槽 flap 冷却（env ARB_PAPER_AUTHORIZED_REOPEN_COOLDOWN_HOURS，默认 24h）。
	ReopenCooldown time.Duration
}

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
//	不误记。
//
// D-410 S4：basis 接 fin_xasset_monitor 面——开仓 entry=XAssetMonitorRowNear（ts<=entry
// 谓词，D-404 R1 已修同冻结锚同源）；每轮 last=LatestXAssetEntryGate 最新行（maxAge 陈旧闸
// 行龄超限→NULL 诚实缺）；退出冻结 convergence=dir×(last−entry)×qty/1e4。monitor 行缺席 →
// basis NULL 不臆造（可算不可毕业）。保底段先行：enabled∧非 simulated 白名单组合不套 net
// 下限开仓（不占 PaperCfdMaxActive 槽），flap 冷却按 max(exit_ts) 重构，双 CEX 组合跳过，
// 宇宙行缺席 warn 节流每轮至多一条。
func StepPaperCfdCarry(ctx context.Context, st PaperStore, rows []UniverseRow, now time.Time, opts PaperCfdOpts) (opened []string, exited int, err error) {
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
	for _, p := range active {
		activeByKey[p.Venue+"|"+p.Symbol] = struct{}{}
	}
	// clean cfd 行索引（保底段与 top-N 段共用；D-345 suspect 行拒入同 top-N 口径）。
	rowByKey := make(map[string]UniverseRow, len(rows))
	for _, r := range rows {
		if cfdStructures[r.Structure] && r.Note == "" {
			rowByKey[r.MtVenue+"×"+r.CexVenue+"|"+r.Canonical] = r
		}
	}
	// D-410 S4 钉死①开票顺序：保底段先行——enabled∧非 simulated 白名单组合不套 net 下限
	// （衰减授权线的证据同样要记），不占 PaperCfdMaxActive 槽；top-N 段经 activeByKey 自动跳过。
	combos, err := st.LoadXAssetAllowCombos(ctx, "real")
	if err != nil {
		return nil, 0, fmt.Errorf("cfd paper step: load allow combos: %w", err)
	}
	type authSlot struct{ key, venueCombo, symbol, mtVenue string }
	slots := []authSlot{}
	for _, c := range combos {
		mtV, cexV, mtCanon, ok := cfdComboLegs(c)
		if !ok {
			// 钉死③：venue 对无 MT 腿（双 CEX 组合）→ 跳过保底+detail note（无映射意义不硬塞）
			log.Printf("[cfd paper] 保底槽跳过双CEX组合 venue=%s/%s canon=%s/%s", c.VenueBuy, c.VenueSell, c.CanonicalBuy, c.CanonicalSell)
			continue
		}
		symbol := NormalizeJoinKey(mtCanon)
		if symbol == "" {
			continue
		}
		slots = append(slots, authSlot{key: mtV + "×" + cexV + "|" + symbol, venueCombo: mtV + "×" + cexV, symbol: symbol, mtVenue: mtV})
	}
	authorized := make(map[string]struct{}, len(slots))
	for _, sl := range slots {
		authorized[sl.key] = struct{}{}
	}
	cfdActive, guaranteedActive := 0, 0
	for _, p := range active {
		if _, ok := authorized[p.Venue+"|"+p.Symbol]; ok {
			guaranteedActive++
		} else if cfdStructures[p.Structure] {
			cfdActive++
		}
	}
	var missing []string
	for _, sl := range slots {
		if _, ok := activeByKey[sl.key]; ok {
			continue
		}
		if guaranteedActive >= opts.AuthorizedMaxActive {
			break
		}
		// 钉死②flap 冷却：退出行 max(exit_ts) 重构窗，冷却内不重开（列已在表上零新表）——
		// 衰减组合 LowStreak 退出→下轮即重开会反复记 est_fee，系统性高估费用冲刷证据流。
		if opts.ReopenCooldown > 0 {
			lastExit, err := st.LatestPaperExitTs(ctx, sl.venueCombo, sl.symbol)
			if err != nil {
				return opened, exited, fmt.Errorf("cfd paper step: exit ts %s: %w", sl.key, err)
			}
			if lastExit != nil && now.Sub(*lastExit) < opts.ReopenCooldown {
				continue
			}
		}
		r, ok := rowByKey[sl.key]
		if !ok {
			missing = append(missing, sl.key) // 宇宙行缺席：不开仓+warn 节流
			continue
		}
		if err := insertCfdPaper(ctx, st, r, sl.mtVenue, sl.venueCombo, now, opts.CexFeeBps); err != nil {
			if err == pgstore.ErrPaperExists {
				continue
			}
			return opened, exited, fmt.Errorf("cfd paper step: open authorized %s: %w", sl.key, err)
		}
		activeByKey[sl.key] = struct{}{}
		guaranteedActive++
		opened = append(opened, r.Canonical)
	}
	if len(missing) > 0 {
		// 宇宙行缺席 warn 节流：每轮至多一条聚合告警（禁 `_ =` 吞错）。
		if err := st.InsertAlert(ctx, nil, "warn", "[cfd 纸面保底] 授权组合宇宙行缺席（无 funding/swap fact，本轮不开仓）: "+strings.Join(missing, ", ")); err != nil {
			log.Printf("[cfd paper] authorized-miss alert: %v", err)
		}
	}
	// ① 开仓（top-N 段）
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
	topN := 0
	for _, c := range cands {
		if cfdActive+topN >= PaperCfdMaxActive { // 保底槽不占此额（钉死①）
			break
		}
		if err := insertCfdPaper(ctx, st, c.r, c.r.MtVenue, c.venueCombo, now, opts.CexFeeBps); err != nil {
			if err == pgstore.ErrPaperExists {
				continue
			}
			return opened, exited, fmt.Errorf("cfd paper step: open %s: %w", c.venueCombo, err)
		}
		topN++
		opened = append(opened, c.r.Canonical)
	}
	// ② 累积 + ③ 退出（仅 cfd 行；spot 轨行不归本函数）
	for _, p := range accrable {
		if !cfdStructures[p.Structure] {
			continue
		}
		// D-410 S4：last_basis 每轮更 monitor 最新行（陈旧闸行龄超限→nil 诚实缺写 NULL）。
		var lastBasis *float64
		pair := paperCfdPairKey(p.Symbol)
		// D-423：三元组 venue 键——Venue="mt×cex" 组合串拆回裸名再映射（mt 列备份拆分缺）。
		mtV, cexV := "", ""
		if i := strings.Index(p.Venue, "×"); i >= 0 {
			mtV, cexV = p.Venue[:i], p.Venue[i+len("×"):]
		}
		cexVenue, mtVenue := monitorVenueKeys(cexV, mtV)
		entryBps, _, _, _, ok, err := st.LatestXAssetEntryGate(ctx, pair, cexVenue, mtVenue, paperCfdMonitorMaxAge)
		if err != nil {
			return opened, exited, fmt.Errorf("cfd paper step: monitor gate %s: %w", pair, err)
		}
		if ok {
			lastBasis = &entryBps
		}
		r, ok := rowByKey[p.Venue+"|"+p.Symbol]
		if !ok {
			if p.Status != "active" {
				continue // demo 行缺席：无数据不误记
			}
			// active 行缺席=信号死：miss 计 streak 同轨，零累积（accruedAdd=0，时钟照走）
			if p.LowStreak+1 >= paperExitStreakRounds {
				if err := st.ExitPaperSpotCarry(ctx, p.ID, "normalized", paperExitConvergence(p, lastBasis)); err != nil {
					return opened, exited, fmt.Errorf("cfd paper step: exit miss %d: %w", p.ID, err)
				}
				exited++
				continue
			}
			prev := 0.0
			if p.LastFundingAnn != nil {
				prev = *p.LastFundingAnn
			}
			if err := st.AccruePaperSpotCarry(ctx, p.ID, prev, 0, p.LowStreak+1, lastBasis); err != nil {
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
			if err := st.AccruePaperSpotCarry(ctx, p.ID, r.FundingAnn, accruedAdd, p.LowStreak, lastBasis); err != nil {
				return opened, exited, fmt.Errorf("cfd paper step: accrue demo %d: %w", p.ID, err)
			}
			continue
		}
		if streak >= paperExitStreakRounds || now.Sub(p.EntryTs).Hours()/24 >= paperMaxAgeDays {
			reason := "normalized"
			if streak < paperExitStreakRounds {
				reason = "age_7d"
			}
			if err := st.ExitPaperSpotCarry(ctx, p.ID, reason, paperExitConvergence(p, lastBasis)); err != nil {
				return opened, exited, fmt.Errorf("cfd paper step: exit %d: %w", p.ID, err)
			}
			exited++
			continue
		}
		if err := st.AccruePaperSpotCarry(ctx, p.ID, r.FundingAnn, accruedAdd, streak, lastBasis); err != nil {
			return opened, exited, fmt.Errorf("cfd paper step: accrue %d: %w", p.ID, err)
		}
	}
	return opened, exited, nil
}
