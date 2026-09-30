// account_live_test.go —— 3d-P1#1c：liveAccountLatest 实时态适配锚 + 测试播种面。
package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/keydomain"
	"arb/internal/store/pgstore"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// liveRowsAcctMon 快照行种子 → AccountMonitor 已发射态（m.cur 直填；
// UpdatedAt=FetchedAt 保留陈旧语义——snapshotStaleWindow 判据不变）。
func liveRowsAcctMon(rows []pgstore.VenueAccountLatest) *AccountMonitor {
	m := NewAccountMonitor(NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute), nil)
	m.cur = map[string]*v1.AccountInfo{}
	for _, r := range rows {
		at := "cex"
		if r.VenueType == "mt4" || r.VenueType == "mt5" {
			at = "cfd"
		}
		info := &v1.AccountInfo{
			VenueKey: keydomain.Join(r.Venue, r.VenueType), Venue: r.Venue,
			VenueType: r.VenueType, AssetType: at, Currency: r.Currency,
			UpdatedAt: timestamppb.New(r.FetchedAt),
		}
		if r.BalanceRaw != nil {
			info.Balance = *r.BalanceRaw
		}
		if r.EquityRaw != nil {
			info.Equity = *r.EquityRaw
		}
		if r.FreeMarginRaw != nil {
			info.FreeMargin = *r.FreeMarginRaw
		} else if r.EquityRaw != nil {
			// proto 无 NULL——旧列 NULL（free 未采→消费方回退净值）的最近语义=free=equity。
			info.FreeMargin = *r.EquityRaw
		}
		if r.BufferMultiple != nil {
			info.BufferMultiple = *r.BufferMultiple
		}
		m.cur[info.VenueKey] = info
	}
	return m
}

// seedLiveAccounts 旧「快照表种子」测试适配：实时态灌入 acctMon（可叠加合并——
// acctMon 已有时按 venueKey merge）+ cfd venue 补真 realKeys
// （fakeGradDB→f.realKeys；真库 store→broker_accounts is_real upsert）。
func seedLiveAccounts(t *testing.T, s *Service, rows []pgstore.VenueAccountLatest) {
	t.Helper()
	if s.acctMon == nil {
		s.acctMon = liveRowsAcctMon(nil)
	}
	for vk, info := range liveRowsAcctMon(rows).cur {
		s.acctMon.cur[vk] = info
	}
	for _, r := range rows {
		if r.VenueType != "mt4" && r.VenueType != "mt5" {
			continue
		}
		if f, ok := s.gradStore.(*fakeGradDB); ok {
			if f.realKeys == nil {
				f.realKeys = map[string]bool{}
			}
			f.realKeys[r.Venue] = true
			continue
		}
		if s.store != nil {
			if _, err := s.store.Pool().Exec(context.Background(), `
				INSERT INTO broker_accounts (venue, venue_type, host, port, password, is_real)
				VALUES ($1,'mt5','h',443,'p',true)
				ON CONFLICT (venue) DO UPDATE SET is_real=true`, r.Venue); err != nil {
				t.Fatalf("seed real key %s: %v", r.Venue, err)
			}
		}
	}
}

// TestLiveAccountLatest 两账户（mt cfd + cex）字段映射 + sim 过滤 + realKeys fail-closed。
func TestLiveAccountLatest(t *testing.T) {
	bal, eq, fm, buf := 1000.0, 1100.0, 900.0, 2.4
	rows := []pgstore.VenueAccountLatest{
		{Venue: "3001157", VenueType: "mt5", BalanceRaw: &bal, EquityRaw: &eq, FreeMarginRaw: &fm, Currency: "USD", FetchedAt: time.Now()},
		{Venue: "okx", VenueType: "perp", EquityRaw: &eq, Currency: "USDT", BufferMultiple: &buf, FetchedAt: time.Now()},
	}
	f := &fakeGradDB{}
	s := &Service{gradStore: f}
	seedLiveAccounts(t, s, rows)
	// cex sim 行应被过滤
	s.acctMon.cur["okxsim:perp"] = &v1.AccountInfo{
		VenueKey: "okxsim:perp", Venue: "okxsim", VenueType: "perp",
		AssetType: "cex", Simulated: true, Equity: 1,
	}
	got, err := s.liveAccountLatest(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("rows=%v err=%v want 2", got, err)
	}
	byV := map[string]pgstore.VenueAccountLatest{}
	for _, r := range got {
		byV[keydomain.Join(r.Venue, r.VenueType)] = r
	}
	mt := byV["3001157:mt5"]
	if mt.EquityRaw == nil || *mt.EquityRaw != 1100 || *mt.FreeMarginRaw != 900 || mt.BufferMultiple != nil {
		t.Errorf("mt 行映射错: %+v", mt)
	}
	cex := byV["okx:unified"] // NormalizeVenueType：okx perp→unified
	if cex.VenueType != "unified" || cex.BufferMultiple == nil || *cex.BufferMultiple != 2.4 {
		t.Errorf("cex 行映射错: %+v", cex)
	}
	// For：命中/未命中
	l, ok, err := s.liveAccountLatestFor(context.Background(), "okx", "unified")
	if !ok || err != nil || l == nil {
		t.Fatalf("For okx/unified miss: %v %v", ok, err)
	}
	if _, ok, _ := s.liveAccountLatestFor(context.Background(), "gate", "perp"); ok {
		t.Error("gate:perp 不应命中")
	}
	// realKeys 丢 → cfd 行丢（fail-closed）但 cex 行留
	f.realKeys = nil
	got2, _ := s.liveAccountLatest(context.Background())
	for _, r := range got2 {
		if r.Venue == "3001157" {
			t.Errorf("realKeys nil 时 cfd 应丢: %+v", r)
		}
	}
	// 无 acctMon（部分构造）→ 空,nil
	s2 := &Service{gradStore: f}
	if got3, err := s2.liveAccountLatest(context.Background()); err != nil || len(got3) != 0 {
		t.Errorf("nil acctMon → rows=%v err=%v want 空,nil", got3, err)
	}
}

// TestKickNostreamAccounts #1c S3：无流子账户（funding/earn）Kick 定向回读，
// 有流键（okx spot/perp→unified 私有流覆盖）不 Kick；10s/键防抖。
func TestKickNostreamAccounts(t *testing.T) {
	s := &Service{}
	seedLiveAccounts(t, s, []pgstore.VenueAccountLatest{
		{Venue: "binance", VenueType: "funding", EquityRaw: f64p(1)},
		{Venue: "okx", VenueType: "earn", EquityRaw: f64p(1)},
		{Venue: "okx", VenueType: "perp", EquityRaw: f64p(1)}, // HasPrivateStream → 不踢
		{Venue: "binance", VenueType: "perp", EquityRaw: f64p(1)},
		{Venue: "3001157", VenueType: "mt5", EquityRaw: f64p(1)}, // 非 cex → 不踢
	})
	kicked := func() map[string]bool {
		s.acctMon.mu.Lock()
		defer s.acctMon.mu.Unlock()
		out := map[string]bool{}
		for k := range s.acctMon.pendingKick {
			out[k] = true
		}
		return out
	}
	s.kickNostreamAccounts()
	got := kicked()
	for _, want := range []string{"binance:funding", "okx:earn"} {
		if !got[want] {
			t.Errorf("无流键 %s 未被 Kick: %v", want, got)
		}
	}
	for _, no := range []string{"okx:perp", "binance:perp", "3001157:mt5"} {
		if got[no] {
			t.Errorf("有流/非 cex 键 %s 不应被 Kick", no)
		}
	}
	// 防抖：清 pending 后立即再调 → 不再入队。
	s.acctMon.mu.Lock()
	s.acctMon.pendingKick = map[string]struct{}{}
	s.acctMon.mu.Unlock()
	s.kickNostreamAccounts()
	if len(kicked()) != 0 {
		t.Errorf("10s 防抖内重复 Kick: %v", kicked())
	}
	// 越过防抖窗 → 再 Kick。
	s.acctKickAt["binance:funding"] = time.Now().Add(-11 * time.Second)
	s.kickNostreamAccounts()
	if !kicked()["binance:funding"] {
		t.Error("防抖窗过后应再 Kick binance:funding")
	}
}
