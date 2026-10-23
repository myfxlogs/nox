// live_exact.go —— D-130 域1：展示面账户行精确键读口（自 capitalview.go 拆出控行数）。
// 只认精确 venue+type 键（活账本单键→精确 REST 校准→快照精确行），查无→false fail-closed；
// PoolKeys/SnapPick 回退链=持仓腿资金池语义，账户面行回退即错账——腿级径不落此。
package capitalview

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/data/keydomain"
	"nox/internal/data/venue"
	"nox/internal/store/pgstore"
)

// LiveSnapsExact D-130 域1：展示面账户行专用读口——只认精确 venue+type 键
// （活账本单键→精确 REST 校准→快照精确行，三径同规），查无→false（调用方「未采」fail-closed）。
// PoolKeys/SnapPick 回退链（精确→unified→perp→任意非空）=持仓腿资金池语义，
// 账户面行回退即错账（生产实证 okx funding/earn 行撞 unified 同值 2034.32）——腿级径不落此。
func (v *View) LiveSnapsExact(ctx context.Context, venueKey string, snaps []pgstore.VenueAccountLatest) (Funds, bool) {
	return v.liveExact(ctx, venueKey, snaps, false)
}

func (v *View) liveExact(ctx context.Context, venueKey string, snaps []pgstore.VenueAccountLatest, force bool) (Funds, bool) {
	k := keydomain.Parse(venueKey)
	exactKey := keydomain.Join(k.Venue, k.Type)
	if IsCEXVenue(venueKey) {
		if v.d.Ledger != nil {
			if e, ok := v.d.Ledger.Get(exactKey, v.restTTL(), time.Now()); ok {
				if !force || e.Src != SrcRest {
					return liveFunds(e), true
				}
			}
		}
		if f, ok := v.calibrate(ctx, venueKey, force, true); ok {
			return f, true
		}
	} else if f, ok := v.mtFrame(venueKey); ok {
		return f, true // D-572 S2：MT 键先走流帧；超窗/miss 落快照精确行
	}
	if snaps == nil {
		if v.d.Snapshots == nil {
			return Funds{}, false
		}
		var err error
		if snaps, err = v.d.Snapshots(ctx); err != nil {
			return Funds{}, false
		}
	}
	for _, r := range snaps { // 快照精确行（unified/perp/任意行回退禁用）
		if r.Venue == k.Venue && r.VenueType == k.Type && r.EquityRaw != nil {
			return SnapFundsOf(r, time.Now()), true
		}
	}
	return Funds{}, false
}

func (v *View) live(ctx context.Context, venueKey string, snaps []pgstore.VenueAccountLatest, force bool) (Funds, bool) {
	if IsCEXVenue(venueKey) {
		k := keydomain.Parse(venueKey)
		if v.d.Ledger != nil {
			for _, key := range PoolKeys(k.Venue, k.Type) {
				if e, ok := v.d.Ledger.Get(key, v.restTTL(), time.Now()); ok {
					// force=决策面拒收 rest TTL 内校准值（变相快照）；
					// ws 实推条目非缓存语义——仍认 live 直读。
					if !force || e.Src != SrcRest {
						return liveFunds(e), true
					}
				}
			}
		}
		// 账本未装配也走 REST 校准（执行面裁定：REST 败才 fail-closed，不因账本缺席直落快照）。
		if f, ok := v.calibrate(ctx, venueKey, force, false); ok {
			return f, true
		}
	} else if f, ok := v.mtFrame(venueKey); ok {
		return f, true // D-572 S2：MT 键先走流帧（fresh 即 live，龄闸不触发）；超窗/miss 落快照兜底
	}
	if snaps == nil {
		if v.d.Snapshots == nil {
			return Funds{}, false
		}
		var err error
		if snaps, err = v.d.Snapshots(ctx); err != nil {
			return Funds{}, false
		}
	}
	r, ok := SnapPick(snaps, venueKey)
	if !ok {
		return Funds{}, false
	}
	return SnapFundsOf(r, time.Now()), true
}

// writeCalibSnapshot D-523①：REST 校准成功且值变 → fin_venue_account_snapshot
// src=rest_calib（活账本自愈同愈快照读面；WS 覆盖键同径——同值经 calibBase/
// 展示态两层去重零写）。写败只 log 基准不前进。
func (v *View) writeCalibSnapshot(ctx context.Context, poolKey string, avail, eq float64, ccy string, at time.Time, simulated bool) {
	if v.d.SnapWrite == nil || simulated { // sim 行不落库（同 buildVenueSnapshotRows 证真过滤）
		return
	}
	k := keydomain.Parse(poolKey)
	vt := venue.NormalizeVenueType("cex", k.Venue, k.Type)
	row := pgstore.VenueAccountSnapshot{
		Venue: k.Venue, VenueType: vt,
		BalanceRaw: avail, EquityRaw: eq, FreeMarginRaw: avail,
		Currency: ccy, FetchedAt: at, Src: "rest_calib",
	}
	v.calibMu.Lock()
	prev, ok := v.calibBase[poolKey]
	if ok && prev.BalanceRaw == row.BalanceRaw && prev.EquityRaw == row.EquityRaw &&
		prev.Currency == row.Currency {
		v.calibMu.Unlock()
		return // 同值零写
	}
	v.calibMu.Unlock()
	if err := v.d.SnapWrite(ctx, []pgstore.VenueAccountSnapshot{row}); err != nil {
		log.Printf("[capitalview] %s rest_calib snapshot write: %v", poolKey, err)
		return
	}
	v.calibMu.Lock()
	if v.calibBase == nil {
		v.calibBase = map[string]pgstore.VenueAccountSnapshot{}
	}
	v.calibBase[poolKey] = row
	v.calibMu.Unlock()
}

func (v *View) restTTL() time.Duration {
	if v.d.RestTTL != nil {
		return v.d.RestTTL()
	}
	return DefaultRestTTL
}

// privWsGate 私面 WS 化闸（okx 先行）：事件快照新→免 REST 校准（快照行同源供）；
// 陈→监护缺 critical 告警 fail-closed 返回（不静默降级 REST）。gated=false 放行。
func (v *View) privWsGate(ctx context.Context, venueKey string, k keydomain.VenueKey) (f Funds, gated, ok bool) {
	if v.d.PrivWsFresh == nil || k.Venue != "okx" {
		return Funds{}, false, false
	}
	probeType := k.Type
	if probeType != "unified" {
		probeType = "unified" // okx 统一账户制：非 unified 键同探 unified 源（键域归一——写面只落 unified）
	}
	ts, has, perr := v.d.PrivWsFresh(ctx, k.Venue, probeType)
	if perr != nil {
		log.Printf("[capitalview] %s 私面 ws 新度读败 fail-closed: %v", venueKey, perr)
		return Funds{}, true, false
	}
	if has && time.Since(ts) < 5*time.Minute {
		v.privAlerted.Delete(venueKey) // 恢复清报态（状态跳变域——再缺再报）
		if sf, sok := v.snapshotFunds(ctx, venueKey); sok {
			return sf, true, true // WS 活=免 REST（快照行已供）
		}
	}
	// 告警节流（返修①③）：alerted 集命中=状态已报不再重——跳变才报（恢复后
	// ws 新→下次闸过即清 alerted，再缺再报=状态跳变域）。
	if _, alerted := v.privAlerted.Load(venueKey); !alerted {
		msg := fmt.Sprintf("okx 私面监护 WS 事件缺监护（venue=%s type=%s 快照陈/缺）——REST 闸关不降级", venueKey, k.Type)
		if v.d.AlertFn != nil {
			v.d.AlertFn("critical", msg)
		}
		log.Printf("[capitalview] %s", msg)
		v.privAlerted.Store(venueKey, true)
	}
	return Funds{}, true, false
}

// snapshotFunds WS 事件快照行→Funds（okx_event 活径免 REST——同键快照读面）。
func (v *View) snapshotFunds(ctx context.Context, venueKey string) (Funds, bool) {
	if v.d.Snapshots == nil {
		return Funds{}, false
	}
	snaps, err := v.d.Snapshots(ctx)
	if err != nil {
		return Funds{}, false
	}
	k := keydomain.Parse(venueKey)
	for _, r := range snaps {
		if r.Venue == k.Venue && r.VenueType == k.Type && r.EquityRaw != nil {
			return SnapFundsOf(r, time.Now()), true
		}
	}
	return Funds{}, false
}
