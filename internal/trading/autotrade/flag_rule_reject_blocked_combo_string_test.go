// d475a_flags_test.go —— D-475a S2/A2 旗标执行投影验收锚：
// blocked 组合串旗标拒执行（四元 venue 集含组合串两序）；warn 放行；nil 快照放行；'*' 通配。
package autotrade

import (
	"testing"

	"nox/internal/store/pgstore"
)

func d475aSnap(flags ...pgstore.VenueSymbolFlag) *flagSnapshot {
	s := &flagSnapshot{
		blocked: map[string]pgstore.VenueSymbolFlag{},
		warned:  map[string]pgstore.VenueSymbolFlag{},
	}
	for _, f := range flags {
		if f.Level == "warn" {
			s.warned[pgstore.FlagKey(f.Venue, f.Symbol)] = f
		} else {
			s.blocked[pgstore.FlagKey(f.Venue, f.Symbol)] = f
		}
	}
	return s
}

func d475aOpp() pgstore.Opportunity {
	sell := "XAUUSDT"
	return pgstore.Opportunity{
		Type: "XASSET_CARRY", Canonical: "XAUUSD", CanonicalSell: &sell,
		VenueBuy: "3001157:mt5", VenueSell: "okx:perp",
	}
}

func TestD475a_FlagRuleReject_BlockedComboString(t *testing.T) {
	e := &Engine{}
	opp := d475aOpp()
	// 组合串旗标（reject 写口实落口径 '3001157×okx'）——四元集必须捕到，两序皆测。
	for _, v := range []string{"3001157×okx", "okx×3001157"} {
		e.flags.Store(d475aSnap(pgstore.VenueSymbolFlag{Venue: v, Symbol: "XAUUSD", Level: "blocked", Reason: "坑"}))
		if got := e.flagRuleReject(opp); got == "" {
			t.Fatalf("venue=%s 组合串旗标应拒执行（四元集哑火=P0 回归）", v)
		}
	}
	// 卖腿 canonical（XAUUSDT）维命中也拒。
	e.flags.Store(d475aSnap(pgstore.VenueSymbolFlag{Venue: "okx", Symbol: "XAUUSDT", Level: "blocked"}))
	if e.flagRuleReject(opp) == "" {
		t.Fatal("卖腿 canonical 命中 blocked 应拒")
	}
	// venue 级 '*' 通配拒。
	e.flags.Store(d475aSnap(pgstore.VenueSymbolFlag{Venue: "okx", Symbol: "*", Level: "blocked"}))
	if e.flagRuleReject(opp) == "" {
		t.Fatal("'*' 通配 blocked 应拒")
	}
}

func TestD475a_FlagRuleReject_WarnAllows(t *testing.T) {
	e := &Engine{}
	e.flags.Store(d475aSnap(pgstore.VenueSymbolFlag{Venue: "okx", Symbol: "XAUUSD", Level: "warn"}))
	if got := e.flagRuleReject(d475aOpp()); got != "" {
		t.Fatalf("warn 旗标应放行（只 log），got %q", got)
	}
	// 无干旗标不拦。
	e.flags.Store(d475aSnap(pgstore.VenueSymbolFlag{Venue: "binance", Symbol: "BTC", Level: "blocked"}))
	if got := e.flagRuleReject(d475aOpp()); got != "" {
		t.Fatalf("无干旗标不应拦，got %q", got)
	}
}

func TestD475a_FlagRuleReject_NilSnapshot(t *testing.T) {
	e := &Engine{} // flags 未载→nil 快照=空集放行（boot 先载+fatal 兜底，缺席≠拦）
	if got := e.flagRuleReject(d475aOpp()); got != "" {
		t.Fatalf("nil 快照应放行，got %q", got)
	}
}
