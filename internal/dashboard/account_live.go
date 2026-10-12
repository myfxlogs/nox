// account_live.go —— 3d-P1#1c：展示/门禁面账户资金改读 AccountMonitor 实时态
// （业主令：拒绝快照延时信息）。fin_venue_account_snapshot 降为历史记录/审计留痕——
// 唯一实时源 = AccountMonitor（CEX 私有流 + MT 余额帧 + 无流子账户按需回读 Kick）。
package dashboard

import (
	"context"
	"fmt"
	"time"

	"arb/internal/gen/dashboardv1"
	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
	"arb/internal/venue"
)

// liveAccountLatest 从 AccountMonitor 实时态派生 VenueAccountLatest 视图（零 DB 资金读；
// 行口径与 cexscan.buildVenueSnapshotRows 同源：venue.IsRealAccount 过滤 +
// venue.NormalizeVenueType 归一 + 同 (venue,venue_type) 去重仅留首行）。FetchedAt=
// updated_at（实时态帧/回读时刻；无流子账户久未回读会自然显陈旧=诚实「缺数据」）。
// realKeys 经 gradDB（生产=s.store，测试替身 seam 同 gradDB 惯例）；键集查询失败
// fail-closed 整批丢（同写侧口径）。acctMon/store 双 nil（部分构造 Service）→ 空,nil。
func (s *Service) liveAccountLatest(ctx context.Context) ([]pgstore.VenueAccountLatest, error) {
	if s.acctMon == nil || (s.gradStore == nil && s.store == nil) {
		return nil, nil
	}
	realKeys, err := s.gradDB().BrokerRealAccountKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("live account real keys: %w（fail-closed 整批丢）", err)
	}
	infos := s.acctMon.Snapshot()
	out := make([]pgstore.VenueAccountLatest, 0, len(infos))
	seen := make(map[string]bool, len(infos))
	for _, info := range infos {
		if info == nil {
			continue
		}
		l, ok := liveAccountRow(info, realKeys, seen)
		if !ok {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}

// liveAccountLatestFor 单 (venue, venueType) 行（venueType=归一后口径，同写侧）；
// 未命中/被过滤 → ok=false。
func (s *Service) liveAccountLatestFor(ctx context.Context, venue_, vt string) (*pgstore.VenueAccountLatest, bool, error) {
	if s.acctMon == nil || (s.gradStore == nil && s.store == nil) {
		return nil, false, nil
	}
	realKeys, err := s.gradDB().BrokerRealAccountKeys(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("live account real keys: %w（fail-closed）", err)
	}
	want := keydomain.Join(venue_, vt)
	for _, info := range s.acctMon.Snapshot() {
		if info == nil {
			continue
		}
		l, ok := liveAccountRow(info, realKeys, nil)
		if !ok {
			continue
		}
		if keydomain.Join(l.Venue, l.VenueType) == want {
			return &l, true, nil
		}
	}
	return nil, false, nil
}

// acctNostreamKickTTL 无流子账户按需回读防抖窗（Kick 是定向 REST 回读，非周期任务）。
const acctNostreamKickTTL = 10 * time.Second

// kickNostreamAccounts 资金页打开 → 对快照视图中无私有流覆盖的 CEX 键
// （venue.HasPrivateStream=false：okx funding/earn 等无 WS 余额频道
// 子账户，D-006 已举证）定向 Kick 回读；10s/键防抖。RPC 立即返回当前态，回读完成后
// acctTopic 事件→StreamUpdates 推前端刷新（推路径既有）。
func (s *Service) kickNostreamAccounts() {
	if s.acctMon == nil {
		return
	}
	s.acctKickMu.Lock()
	defer s.acctKickMu.Unlock()
	if s.acctKickAt == nil {
		s.acctKickAt = map[string]time.Time{}
	}
	now := time.Now()
	for _, info := range s.acctMon.Snapshot() {
		if info == nil || info.GetAssetType() != "cex" {
			continue
		}
		if venue.HasPrivateStream(info.GetVenue(), info.GetVenueType(), info.GetSimulated()) {
			continue
		}
		vk := info.GetVenueKey()
		if vk == "" {
			vk = keydomain.Join(info.GetVenue(), info.GetVenueType())
		}
		if now.Sub(s.acctKickAt[vk]) < acctNostreamKickTTL {
			continue
		}
		s.acctKickAt[vk] = now
		s.acctMon.Kick(vk)
	}
}

// liveAccountRow 单条 AccountInfo → VenueAccountLatest（seen 非 nil 时承担同键去重）。
func liveAccountRow(info *dashboardv1.AccountInfo, realKeys map[string]bool, seen map[string]bool) (pgstore.VenueAccountLatest, bool) {
	if !venue.IsRealAccount(info.GetAssetType(), info.GetSimulated(), info.GetVenue(), realKeys) {
		return pgstore.VenueAccountLatest{}, false
	}
	vt := venue.NormalizeVenueType(info.GetAssetType(), info.GetVenue(), info.GetVenueType())
	key := keydomain.Join(info.GetVenue(), vt)
	if seen != nil {
		if seen[key] {
			return pgstore.VenueAccountLatest{}, false
		}
		seen[key] = true
	}
	bal, eq, fm := info.GetBalance(), info.GetEquity(), info.GetFreeMargin()
	row := pgstore.VenueAccountLatest{
		Venue: info.GetVenue(), VenueType: vt,
		BalanceRaw: &bal, EquityRaw: &eq, FreeMarginRaw: &fm,
		Currency:  info.GetCurrency(),
		FetchedAt: info.GetUpdatedAt().AsTime(),
	}
	if b := info.GetBufferMultiple(); b > 0 {
		row.BufferMultiple = &b
	}
	return row, true
}
