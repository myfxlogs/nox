// live_exact.go —— D-130 域1：展示面账户行精确键读口（自 capitalview.go 拆出控行数）。
// 只认精确 venue+type 键（活账本单键→精确 REST 校准→快照精确行），查无→false fail-closed；
// PoolKeys/SnapPick 回退链=持仓腿资金池语义，账户面行回退即错账——腿级径不落此。
package capitalview

import (
	"context"
	"time"

	"nox/internal/data/keydomain"
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
