// service_capital_pools_test.go —— 资金地图组装锚：币种分组零换算 / CFD 占用保证金 /
// CEX 在跑归因三源 / idle=equity−deployed / ghost 剔合计 / stale·online 判。
package dashboard

import (
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

func poolRow(venue, vt, ccy string, eq, fm float64, src string, at time.Time, ghost bool) pgstore.CapitalPoolRow {
	return pgstore.CapitalPoolRow{Venue: venue, VenueType: vt, Equity: eq, FreeMargin: fm,
		Currency: ccy, Src: src, FetchedAt: at, Ghost: ghost}
}

// TestCapitalPools_Build 全口径锚。
func TestCapitalPools_Build(t *testing.T) {
	s := &Service{}
	now := time.Now()
	fresh := now.Add(-time.Minute)
	old := now.Add(-time.Hour)

	pools := []pgstore.CapitalPoolRow{
		poolRow("m1", "mt5", "USD", 5000, 4500, "mt_frame", fresh, false),   // 占用 500
		poolRow("m2", "mt5", "USD", 1000, 1000, "mt_frame", old, false),     // 全闲置+陈旧
		poolRow("gone", "mt5", "USD", 172, 172, "mt_frame", old, true),      // 幽灵剔合计
		poolRow("cex", "spot", "USDT", 1045, 0, "cex_event", fresh, false),  // +542 DCI +1000 腿
		poolRow("cex", "earn", "USDT", 169, 0, "rest_poll", fresh, false),   // 全额在跑
		poolRow("cex", "perp", "USDT", 2000, 0, "cex_event", fresh, false),  // +1000 腿
		poolRow("sched", "funding", "USDT", 50, 0, "scheduler", old, false), // scheduler 1h 不陈旧
	}
	legs := []pgstore.CapitalDeployedLeg{
		{VenueKey: "cex:perp", PositionID: 7, PosType: "PERP_FUNDING", Amount: 1000},
	}
	dcis := []pgstore.CapitalDCIOrder{
		{ID: 10, Venue: "cex", CCY: "USDT", Qty: 542},
		{ID: 11, Venue: "cex", CCY: "BTC", Qty: 0.01}, // 币种不匹 → 不入 USDT spot 池
	}

	r := s.buildCapitalPools(pools, legs, dcis, now)
	if len(r.Groups) != 2 || r.Groups[0].Currency != "USD" || r.Groups[1].Currency != "USDT" {
		t.Fatalf("币种分组: %+v", r.Groups)
	}
	usd, usdt := r.Groups[0], r.Groups[1]

	// USD 组：5000+1000（幽灵 172 剔）=6000；deployed=500；idle=5500。
	if usd.Equity != 6000 || usd.Deployed != 500 || usd.Idle != 5500 {
		t.Errorf("USD 组: %+v", usd)
	}
	// USDT 组：1045+169+2000+50=3264；deployed=542+169+1000=1711；
	// idle=equity−池内在跑（DCI 池外锁额不双减，终审 G3）=2095。
	if usdt.Equity != 3264 || usdt.Deployed != 1711 || usdt.Idle != 2095 {
		t.Errorf("USDT 组: %+v", usdt)
	}

	byKey := map[string]*struct {
		dep, idle float64
		online    bool
		stale     bool
		parts     int
	}{}
	for _, g := range r.Groups {
		for _, p := range g.Pools {
			byKey[p.Venue+":"+p.VenueType] = &struct {
				dep, idle float64
				online    bool
				stale     bool
				parts     int
			}{p.Deployed, p.Idle, p.Online, p.Stale, len(p.DeployedParts)}
		}
	}
	if v := byKey["m1:mt5"]; v.dep != 500 || v.idle != 4500 || !v.online {
		t.Errorf("m1: %+v", v)
	}
	if v := byKey["m2:mt5"]; v.dep != 0 || v.idle != 1000 || !v.stale || v.online {
		t.Errorf("m2 stale: %+v", v)
	}
	if v := byKey["cex:spot"]; v.dep != 542 || v.idle != 1045 || v.parts != 1 {
		t.Errorf("cex spot DCI 池外在跑（idle 不双减）: %+v", v)
	}
	if v := byKey["cex:earn"]; v.dep != 169 || v.idle != 0 {
		t.Errorf("earn 全额在跑: %+v", v)
	}
	if v := byKey["cex:perp"]; v.dep != 1000 || v.idle != 1000 {
		t.Errorf("perp 腿归因: %+v", v)
	}
	if v := byKey["sched:funding"]; v.stale || !v.online {
		t.Errorf("scheduler 36h 窗: %+v", v)
	}
	// DCI part ref 文本锚。
	for _, g := range r.Groups {
		for _, p := range g.Pools {
			if p.Venue == "cex" && p.VenueType == "spot" {
				if p.DeployedParts[0].Kind != "dci" || p.DeployedParts[0].Ref != "DCI#10（池外锁额）" {
					t.Errorf("dci part: %+v", p.DeployedParts[0])
				}
			}
		}
	}
}
