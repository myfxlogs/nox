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
	// PaperCfdMaxActive 活跃 cfd 纸面「品种多样性」上限——D-548 S1 起按
	// structure+symbol(canonical) 去重计数（同 canonical 多 MT 账户行只占 1 槽，
	// 观察面每账户行照常开/应计/退——US30 六账户吃光 6 槽病灶的根修；
	// 保底槽不占此额同 D-410 S4）。
	PaperCfdMaxActive = 6
	// paperZeroAccrualStaleHours D-548 S2：零收益僵尸槽判据之二——入场超 24h
	// （swap/funding 真 accrue 一个结算日应有量；US30 六行入场 24h+ accrued=$0.00 实证）。
	paperZeroAccrualStaleHours = 24
)

// PaperCfdOpts D-410 S4 装配参数（wire_scan 注入 cfg/env 值，scan 层不读 env）。
type PaperCfdOpts struct {
	// D-474a S2：CexFeeBps 退役——费用改读 venue 真值表（paper_fees.go）。
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
//	无 sideFactor（与 spot 轨公式差异点）；last_funding_ann 存当轮瞬时 funding（展示列，D-034）。
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
// flags：D-463返修R2——blocked 旗标腿级展开硬拦全部入口（保底槽+top-N 同口径，
// warn 不拦）；wire 每轮 ListActiveFlags 一次传入，nil=无旗放行。
func StepPaperCfdCarry(ctx context.Context, st PaperStore, rows []UniverseRow, now time.Time, opts PaperCfdOpts, flags map[string]pgstore.VenueSymbolFlag) (opened []string, exited int, err error) {
	active, err := st.ListActivePaperSpotCarry(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("cfd paper step: list active: %w", err)
	}
	// 累积清单在开仓前加载（同 spot 轨 R1 教训：本轮新开行次轮起算）。
	accrable, err := st.ListAccrablePaperSpotCarry(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("cfd paper step: list accrable: %w", err)
	}
	// D-028 ③：双行 emit（long/short 各一行）→ 在仓视图拆二：
	//   activePairKeys=venue 对维度（保底段在仓判定：同一 venue 对任一结构在仓即占用保底）；
	//   activeStructKeys=结构维度（top-N 段排除：两结构行各自独立开仓互不遮蔽）。
	activePairKeys := make(map[string]struct{}, len(active))
	activeStructKeys := make(map[string]struct{}, len(active))
	rowByPair := make(map[string]UniverseRow, len(rows)) // D-028 ③：venue 对级开仓取行视图
	dirtyPair := make(map[string]string, len(rows))      // 同级 dirty 归因（任一结构 dirty 即剔）
	for _, p := range active {
		activePairKeys[p.VenuePair()+"|"+p.Symbol] = struct{}{}
		activeStructKeys[p.VenuePair()+"|"+p.Symbol+"|"+p.Structure] = struct{}{}
	}
	// clean cfd 行索引（保底段与 top-N 段共用；D-345 suspect 行拒入同 top-N 口径）。
	// D-419 S2：dirtyByKey 并行索引——行在但 Note!=""（suspect/stale 剔除）记原因，
	// 缺席 warn 分级归因（无行 vs 被剔）。
	rowByKey := make(map[string]UniverseRow, len(rows))
	dirtyByKey := make(map[string]string, len(rows))
	// D-043 ②：等价类视图（class 键行→venue 实解析；读失败 fail-open 退化 legacy 键）。
	var view *assetClassView
	if equiv, eerr := st.ListAssetEquiv(ctx); eerr == nil {
		view = newAssetClassView(equiv)
	} else {
		log.Printf("[cfd paper] asset_equiv: %v（退化 legacy 键）", eerr)
	}
	for _, r := range rows {
		if !cfdStructures[r.Structure] {
			continue
		}
		key := r.MtVenue + "×" + r.CexVenue + "|" + r.Canonical + "|" + r.Structure // D-028 ③：结构维度
		pairKey := r.MtVenue + "×" + r.CexVenue + "|" + r.Canonical                 // venue 对级（保底段在/缺席与开仓取行视图）
		if r.Note == "" {
			rowByKey[key] = r
			if _, exists := rowByPair[pairKey]; !exists {
				rowByPair[pairKey] = r // 双行 emit 后取首个（排序 net 降序=更优结构）
			}
			// D-043 ②：class 键行补 MT canonical NJK 键——保底槽键=NJK(mtCanon)（XAU 形）
			// 恒可命中 GOLD/WTI 形 class 行（排序后先写者胜=net 更优行占位）。
			if alt := NormalizeJoinKey(view.mtCanon(r.Canonical, r.MtVenue)); alt != "" && alt != r.Canonical {
				altKey := r.MtVenue + "×" + r.CexVenue + "|" + alt
				if _, exists := rowByPair[altKey]; !exists {
					rowByPair[altKey] = r
				}
			}
		} else {
			dirtyByKey[key] = r.Note
			if _, exists := dirtyPair[pairKey]; !exists {
				dirtyPair[pairKey] = r.Note
			}
		}
	}
	// D-410 S4 钉死①开票顺序：保底段先行——enabled∧非 simulated 白名单组合不套 net 下限
	// （衰减授权线的证据同样要记），不占 PaperCfdMaxActive 槽；top-N 段经 activeStructKeys 自动跳过。
	combos, err := st.LoadXAssetAllowCombos(ctx, "real")
	if err != nil {
		return nil, 0, fmt.Errorf("cfd paper step: load allow combos: %w", err)
	}
	slots := []authSlot{}
	for _, c := range combos {
		mtV, cexV, mtCanon, ok := cfdComboLegs(c)
		if !ok {
			if _, _, _, _, _, dual := cexSpotPerpComboLegs(c); dual {
				// D-555 S1：双CEX spot×perp 同 canonical 构型=cex 纸面轨（StepPaperCexCarry）接管，
				// 此处静默跳过（「保底槽跳过双CEX组合」告警对本构型消失=A1 锚）。
				continue
			}
			// 钉死③：venue 对无 MT 腿且非 spot×perp 构型（双 perp 等）→ 跳过保底+note
			log.Printf("[cfd paper] 保底槽跳过双CEX组合 venue=%s/%s canon=%s/%s", c.VenueBuy, c.VenueSell, c.CanonicalBuy, c.CanonicalSell)
			continue
		}
		symbol := NormalizeJoinKey(mtCanon)
		if symbol == "" {
			continue
		}
		slots = append(slots, authSlot{key: mtV + "×" + cexV + "|" + symbol, venueCombo: mtV + "×" + cexV,
			symbol: symbol, mtVenue: mtV, cexVenue: cexV, cls: view.mtClassOf(mtV, mtCanon)})
	}
	authorized := make(map[string]struct{}, len(slots)*2)
	for _, sl := range slots {
		authorized[sl.key] = struct{}{}
		// D-043 ②：class 键在仓行（Symbol=GOLD 等）同样计授权——guaranteedActive 不漏算。
		if sl.cls != "" {
			authorized[sl.venueCombo+"|"+sl.cls] = struct{}{}
		}
	}
	guaranteedActive := 0
	// D-548 S1：槽额=canonical 去重计数（structure|symbol）——同 canonical 多账户行
	// 合占 1 槽；保底槽不计（authorized 先行判）。
	slotKeys := make(map[string]struct{}, len(active))
	for _, p := range active {
		if _, ok := authorized[p.VenuePair()+"|"+p.Symbol]; ok {
			guaranteedActive++
		} else if cfdStructures[p.Structure] {
			slotKeys[p.Structure+"|"+p.Symbol] = struct{}{}
		}
	}
	cfdActive := len(slotKeys)
	// D-500 S1：venue 静默探测先行（保底 warn 分级要用）——accrable cfd 行的 MT 臂 venue
	// 在本轮 rows 零 cfd 产出行（含 dirty）→ 休市/断供非机会消失。
	mtCfdCount := make(map[string]int)
	for _, r := range rows {
		if cfdStructures[r.Structure] {
			mtCfdCount[r.MtVenue]++
		}
	}
	silentMt := make(map[string]bool)
	for _, p := range accrable {
		if cfdStructures[p.Structure] && p.VenueA != "" && mtCfdCount[p.VenueA] == 0 {
			silentMt[p.VenueA] = true
		}
	}
	for _, sl := range slots {
		if mtCfdCount[sl.mtVenue] == 0 {
			silentMt[sl.mtVenue] = true
		}
	}
	var missing []string
	for _, sl := range slots {
		if _, ok := activePairKeys[sl.key]; ok {
			continue
		}
		if silentMt[sl.mtVenue] {
			continue // D-500：venue 静默=休市常态非异常，不 warn
		}
		if f := blockedFlagHit(sl.mtVenue, sl.cexVenue, sl.symbol, &sl.mtVenue, flags); f != nil {
			log.Printf("[cfd paper] blocked flag 拦截保底槽: %s（%s@%s|%s）", sl.key, f.Reason, f.Venue, f.Symbol)
			continue
		}
		if guaranteedActive >= opts.AuthorizedMaxActive {
			break
		}
		// 钉死②flap 冷却：退出行 max(exit_ts) 重构窗，冷却内不重开（列已在表上零新表）——
		// 衰减组合 LowStreak 退出→下轮即重开会反复记 est_fee，系统性高估费用冲刷证据流。
		if opts.ReopenCooldown > 0 {
			lastExit, err := st.LatestPaperExitTs(ctx, sl.mtVenue, sl.cexVenue, sl.symbol)
			if err != nil {
				return opened, exited, fmt.Errorf("cfd paper step: exit ts %s: %w", sl.key, err)
			}
			if lastExit != nil && now.Sub(*lastExit) < opts.ReopenCooldown {
				continue
			}
		}
		r, ok := rowByPair[sl.key] // D-028 ③：保底=venue 对级取行（双结构任一在场即可开）
		if !ok {
			// D-419 S2：缺席分级归因——行在但被 Note 剔除 vs universe 未产行。
			if note, dirty := dirtyPair[sl.key]; dirty {
				missing = append(missing, sl.key+"（行被剔除:"+note+"）")
			} else {
				missing = append(missing, sl.key+"（universe 未产行）")
			}
			continue
		}
		if err := insertCfdPaper(ctx, st, r, sl.mtVenue, sl.cexVenue, view, now); err != nil {
			if err == pgstore.ErrPaperExists {
				continue
			}
			return opened, exited, fmt.Errorf("cfd paper step: open authorized %s: %w", sl.key, err)
		}
		activePairKeys[sl.key] = struct{}{}
		guaranteedActive++
		opened = append(opened, r.Canonical)
	}
	if len(missing) > 0 {
		// 宇宙行缺席/被剔 warn 节流：每轮至多一条聚合告警（D-419 分级归因；禁 `_ =` 吞错）。
		if err := st.InsertAlert(ctx, nil, "warn", "[cfd 纸面保底] 授权组合宇宙行缺席/被剔（本轮不开仓）: "+strings.Join(missing, ", ")); err != nil {
			log.Printf("[cfd paper] authorized-miss alert: %v", err)
		}
	}
	warnMonitorBookAbsent(ctx, st, slots, view) // D-419 S3：簿覆盖哨兵（不阻断开仓）
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
		if _, ok := activeStructKeys[venueCombo+"|"+r.Canonical+"|"+r.Structure]; ok { // D-028 ③：结构维度
			continue
		}
		if f := blockedFlagHit(r.MtVenue, r.CexVenue, r.Canonical, &r.MtVenue, flags); f != nil {
			log.Printf("[cfd paper] blocked flag 拦截: %s|%s（%s@%s|%s）", venueCombo, r.Canonical, f.Reason, f.Venue, f.Symbol)
			continue
		}
		cands = append(cands, cfdCand{venueCombo: venueCombo, r: r})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].r.NetAnnual > cands[j].r.NetAnnual })
	topN := 0
	for _, c := range cands {
		slotKey := c.r.Structure + "|" + c.r.Canonical
		_, occupied := slotKeys[slotKey]
		if !occupied && cfdActive+topN >= PaperCfdMaxActive {
			// 新 canonical 无槽——不 break（同 canonical 复行不占槽仍可开，
			// 观察面每账户一份语义不变）。
			continue
		}
		if err := insertCfdPaper(ctx, st, c.r, c.r.MtVenue, c.r.CexVenue, view, now); err != nil {
			if err == pgstore.ErrPaperExists {
				continue
			}
			return opened, exited, fmt.Errorf("cfd paper step: open %s: %w", c.venueCombo, err)
		}
		if !occupied {
			slotKeys[slotKey] = struct{}{}
			topN++
		}
		opened = append(opened, c.r.Canonical)
	}
	// ② 累积 + ③ 退出（仅 cfd 行；spot 轨行不归本函数）——D-474a 起拆至 paper_cfd_accrue.go。
	n, err := accrueExitCfdPaper(ctx, st, accrable, rowByKey, dirtyPair, silentMt, view, now) // D-028 ③：dirty=suspend 判定走 venue 对级（结构无关）
	if err != nil {
		return opened, exited, err
	}
	exited += n
	return opened, exited, nil
}
