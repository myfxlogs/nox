// d376_pairs_split_test.go —— D-376 S4 A 面锚：divPair 结构 + 告警分层纯函数（先红后绿）。
package main

import (
	"testing"
)

// TestDivergences_PairStructure 锚A：pairs 结构正确——short=funding 高侧（收割方向）、
// 两侧原始 symbol、net=差值、volMin=两侧小者；输入顺序无关（高侧恒 short）。
func TestDivergences_PairStructure(t *testing.T) {
	mk := func(venue, symbol string, ann float64) normRow {
		return normRow{Row: Row{Symbol: symbol, Vol24h: 6e6}, Venue: venue,
			Canonical: canonicalOf(normBase(symbol)), FundingAnn: fptr(ann)}
	}
	// binance 高侧（short）；输入倒序两次 → 结构恒定
	rows := []normRow{mk("okx", "OP-USDT-SWAP", -16.9), mk("binance", "OPUSDT", 7.6)}
	pairs, _ := divergences(rows, 20, 5e6)
	if len(pairs) != 1 {
		t.Fatalf("锚A: want 1 pair, got %d", len(pairs))
	}
	p := pairs[0]
	if p.Canon != "OP" || p.ShortVenue != "binance" || p.LongVenue != "okx" {
		t.Fatalf("锚A: canon/short/long = %s/%s/%s, want OP/binance/okx（short=高侧）", p.Canon, p.ShortVenue, p.LongVenue)
	}
	if p.ShortSym != "OPUSDT" || p.LongSym != "OP-USDT-SWAP" {
		t.Fatalf("锚A: ShortSym/LongSym = %s/%s, want 原始 symbol（纸面重算反查键）", p.ShortSym, p.LongSym)
	}
	if p.FundShort != 7.6 || p.FundLong != -16.9 || (p.NetAnn-24.5) > 1e-9 || (p.NetAnn-24.5) < -1e-9 {
		t.Fatalf("锚A: fund/net = %v/%v/%v, want 7.6/-16.9/24.5", p.FundShort, p.FundLong, p.NetAnn)
	}
	if p.VolMin != 6e6 {
		t.Fatalf("锚A: volMin = %v, want 6e6", p.VolMin)
	}
	// event 文案渲染：格式不变（Short:Long 在前）
	e := divEvent(p)
	if e.EventType != "FUNDING_DIVERGENCE" || e.Symbol != "OP" ||
		e.Detail != "binance:okx +7.6% vs -16.9% ann" {
		t.Fatalf("锚A: event = %+v（detail 格式须与旧径逐字同型）", e)
	}
}

// TestD376_SplitDivPairs 告警分层纯函数锚：suspect → suspectList；任一侧真/未知 → realPairs
// （digest 化+纸面）；双腿确证 sim → simPairs（既有 event 路径零变更）。
func TestD376_SplitDivPairs(t *testing.T) {
	tr, fa := true, false
	mkPair := func(canon, a, b string) divPair {
		return divPair{Canon: canon, ShortVenue: a, LongVenue: b,
			FundShort: 25, FundLong: 2, NetAnn: 23, VolMin: 6e6}
	}
	pairs := []divPair{
		mkPair("SIMSYM", "binance", "okx"),      // 双 sim
		mkPair("REALSYM", "binance", "gate"),    // gate 真户
		mkPair("UNKNOWNSYM", "binance", "mexc"), // mexc 缺行=未知
		{Canon: "PROVE", ShortVenue: "binance", LongVenue: "okx", FundShort: -593, FundLong: -547, NetAnn: 46, VolMin: 6e6}, // suspect
	}
	simMap := map[string]*bool{"binance:perp": &fa, "okx:perp": &fa, "gate:perp": &tr}
	simPairs, realPairs, suspectList := splitDivPairs(pairs, simMap)
	if len(simPairs) != 1 || simPairs[0].Canon != "SIMSYM" {
		t.Fatalf("分层锚: simPairs = %+v, want [SIMSYM]（双腿确证 sim 走既有 event 路径）", simPairs)
	}
	if len(realPairs) != 2 {
		t.Fatalf("分层锚: realPairs = %+v, want 2（REALSYM 真腿+UNKNOWNSYM 未知保守当真）", realPairs)
	}
	if len(suspectList) != 1 || suspectList[0] != "PROVE" {
		t.Fatalf("分层锚: suspectList = %v, want [PROVE]（极端 funding 压 digest 不开仓）", suspectList)
	}
	// 查询失败空 map → 保守全当真腿（全 digest 化）
	simPairs2, realPairs2, suspectList2 := splitDivPairs(pairs[:3], map[string]*bool{})
	if len(simPairs2) != 0 || len(realPairs2) != 3 || len(suspectList2) != 0 {
		t.Fatalf("分层锚: 空 simMap want 0 sim/3 real, got %d/%d/%d", len(simPairs2), len(realPairs2), len(suspectList2))
	}
}
