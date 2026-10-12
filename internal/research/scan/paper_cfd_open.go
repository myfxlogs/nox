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

// cfdSkipCounts D-050 ②：发现段跳过原因计数（log 口径，cooldown=flap+data_lost 合并；
// ③：uncoverable=簿结构性不可覆盖拒开数）。
type cfdSkipCounts struct {
	cooldown, stale, belowFloor, overBand, uncoverable int
}

// openCfdDiscovery D-050 发现段：干净行（Note==""∧net∈[8,300]）全量按
// (canonical, mt, cex, structure) 开纸面行——数量槽退役，只留质量闸+双冷却
// （flap ReopenCooldown + D-042 data_lost）；blocked flag 逐行 log 不计数。
// ③：簿结构性不可覆盖（CEX 无报价源/合约、MT 保留窗零 tick）→不开记因聚合 warn。
// 计数+candidates 构造与开仓循环自 StepPaperCfdCarry 拆出（文件 ≤300 行）。
func openCfdDiscovery(ctx context.Context, st PaperStore, rows []UniverseRow, activeStructKeys map[string]struct{},
	flags map[string]pgstore.VenueSymbolFlag, view *assetClassView, uni map[string]pgstore.CexUniverseRow,
	now time.Time, opts PaperCfdOpts,
	dataLost func(venueA, venueB, symbol string) bool) ([]string, cfdSkipCounts, error) {
	opened := []string{}
	var sk cfdSkipCounts
	var blocked []string // D-144 ③：簿不可覆盖拒开留痕（聚合 warn 每轮至多一条）
	type cfdCand struct {
		venueCombo string
		r          UniverseRow
	}
	cands := []cfdCand{}
	for _, r := range rows {
		if !cfdStructures[r.Structure] {
			continue
		}
		if r.Note != "" { // D-345 suspect/stale 行拒（质量闸）
			sk.stale++
			continue
		}
		if r.NetAnnual < PaperCfdOpenNetPct { // 既有 net 下限（质量闸）
			sk.belowFloor++
			continue
		}
		if r.NetAnnual > PaperCfdMaxNetPct { // band 上限（长尾区）
			sk.overBand++
			continue
		}
		venueCombo := r.MtVenue + "×" + r.CexVenue
		if _, ok := activeStructKeys[venueCombo+"|"+r.Canonical+"|"+r.Structure]; ok { // D-028 ③：在仓去重（结构维度）
			continue
		}
		if f := blockedFlagHit(r.MtVenue, r.CexVenue, r.Canonical, &r.MtVenue, flags); f != nil {
			log.Printf("[cfd paper] blocked flag 拦截: %s|%s（%s@%s|%s）", venueCombo, r.Canonical, f.Reason, f.Venue, f.Symbol)
			continue
		}
		cands = append(cands, cfdCand{venueCombo: venueCombo, r: r})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].r.NetAnnual > cands[j].r.NetAnnual })
	for _, c := range cands {
		// 冷却中跳过——flap ReopenCooldown + D-042 data_lost 冷却同闸（两段同口径）。
		if opts.ReopenCooldown > 0 {
			lastExit, err := st.LatestPaperExitTs(ctx, c.r.MtVenue, c.r.CexVenue, c.r.Canonical)
			if err != nil {
				return nil, sk, fmt.Errorf("cfd paper step: exit ts %s: %w", c.venueCombo, err)
			}
			if lastExit != nil && now.Sub(*lastExit) < opts.ReopenCooldown {
				sk.cooldown++
				continue
			}
		}
		if dataLost(c.r.MtVenue, c.r.CexVenue, c.r.Canonical) {
			sk.cooldown++
			continue
		}
		// ③：簿结构性不可覆盖→不开记因（测不了的仓不验证）。
		if reason := bookUncoverableReason(ctx, st, uni, view, c.r.Canonical, c.r.MtVenue, c.r.CexVenue, now); reason != "" {
			sk.uncoverable++
			blocked = append(blocked, c.venueCombo+"|"+c.r.Canonical+"（"+reason+"）")
			continue
		}
		if err := insertCfdPaper(ctx, st, c.r, c.r.MtVenue, c.r.CexVenue, view, now); err != nil {
			if err == pgstore.ErrPaperExists {
				continue
			}
			return nil, sk, fmt.Errorf("cfd paper step: open %s: %w", c.venueCombo, err)
		}
		opened = append(opened, c.r.Canonical)
	}
	if len(blocked) > 0 {
		if err := st.InsertAlert(ctx, nil, "warn", "[cfd 纸面发现] 簿结构性不可覆盖拒开（测不了的仓不验证）: "+strings.Join(blocked, ", ")); err != nil {
			log.Printf("[cfd paper] discovery uncoverable alert: %v", err)
		}
	}
	return opened, sk, nil
}
