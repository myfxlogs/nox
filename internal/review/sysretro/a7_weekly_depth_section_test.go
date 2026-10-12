// d578_depth_test.go —— D-578 S2 锚 A7：周报深度表入 prompt/日报快照行/空态注记。
package sysretro

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

func d578Rows() []pgstore.DepthFact {
	ts := time.Date(2026, 9, 23, 20, 0, 0, 0, BJTime)
	var out []pgstore.DepthFact
	add := func(venue, sym, kind string, v float64, i int) {
		out = append(out, pgstore.DepthFact{Venue: venue + ":spot", Symbol: sym, Kind: kind, Value: v, Ts: ts.Add(time.Duration(i) * time.Hour)})
	}
	// binance XAUT 4 拍（满窗）；gate XAUT 2 拍（采样中）；okx XAUT 零行。
	for i := 0; i < 4; i++ {
		add("binance", "XAUT", depthSpreadKind, 2.0+float64(i)*0.1, i)
		add("binance", "XAUT", depthBidKind, 300000+float64(i)*1000, i)
		add("binance", "XAUT", depthAskKind, 410000, i)
	}
	for i := 0; i < 2; i++ {
		add("gate", "XAUT", depthSpreadKind, 3.0, i)
		add("gate", "XAUT", depthBidKind, 420000, i)
		add("gate", "XAUT", depthAskKind, 420000, i)
	}
	return out
}

func TestD578_A7_WeeklyDepthSection(t *testing.T) {
	st := &fakeRetroStore{weekly: &pgstore.RetroWeekly{}, depth: d578Rows()}
	ai := `## 交易成败归因
x
## 纸面交易复盘
x
## 策略生死状
x
## 踩坑与经验
x
## 改进建议
` + "```sysretro-suggestions\n"
	deps := testDeps(t, st, fakeRun(t, "三所深度", ai))
	path, err := GenerateWeekly(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	// 表在 prompt（fenced 数据管道）；同时聚合渲染直测。
	tbl := depthWeeklyTable(st.depth)
	if !strings.Contains(tbl, "binance XAUT n=4") || !strings.Contains(tbl, "spread_bps avg=2.15") {
		t.Fatalf("binance 行聚合错:\n%s", tbl)
	}
	if !strings.Contains(tbl, "gate XAUT n=2（采样中 2 拍）") {
		t.Fatalf("采样中注记缺:\n%s", tbl)
	}
	if !strings.Contains(tbl, "okx XAUT: 无此对或采样行=0") {
		t.Fatalf("okx 空态注记缺:\n%s", tbl)
	}
	_ = path
}

func TestD578_A7_DailyDepthLine(t *testing.T) {
	st := &fakeRetroStore{daily: &pgstore.RetroDaily{
		EvalDist:  map[string]int{"hold": 1},
		AlertDist: map[string]int{},
	}, depth: d578Rows()}
	deps := testDeps(t, st, fakeRun(t, "三所深度当日", "ok"))
	path, err := GenerateDaily(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	txt := string(mustRead(t, path))
	// 当日窗=昨日 BJ 历日 [9-22 16:00Z, 9-23 16:00Z)；d578Rows ts=BJ 9-23 20:00=UTC 12:00 在窗外？核对：
	// ts=BJ20:00 = UTC12:00 于 9-23 —— 落在窗外[9-22 16:00Z,9-23 16:00Z) 内（12:00<16:00 ✓）。
	if !strings.Contains(txt, "三所深度当日") {
		t.Fatalf("日报缺深度行")
	}
	// 渲染直测：binance XAUT 中位 bid=301500（300000/301000/302000/303000 取 index2=302000）……
	// medianOf 取 s[n/2]=index2 → 302000。
	line := depthDailyLine(st.depth)
	if !strings.Contains(line, "binance XAUT 买深$302000/卖深$410000") {
		t.Fatalf("快照行错: %s", line)
	}
	if !strings.Contains(line, "okx XAUT=无此对") {
		t.Fatalf("okx 空态缺: %s", line)
	}
	// 全空窗 → 采样中。
	if got := depthDailyLine(nil); got != "三所深度当日: 采样中（0 拍）" {
		t.Fatalf("空窗=%q", got)
	}
}
