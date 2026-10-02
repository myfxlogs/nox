// d015_external_close_test.go —— D-015 ②③ 验收：回放 #505 时间线。
// venue 平仓事件 → CLOSE EXTERNAL 腿落库 + 残腿集合排除 + Kick 叫醒；
// fastRetry 升级置位 venue 降级态、ResumeClose 成功清零。
package reconcile

import (
	"context"
	"errors"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// d015OpenPos 构造 OPEN 仓 + 单 OPEN FILLED 腿（venue/symbol/side 可控）。
func d015OpenPos(id int64, venue, symbol, side string) (*pgstore.Position, *pgstore.PositionLeg) {
	p := &pgstore.Position{ID: id, OpportunityID: "opp:505", Type: "XASSET_CARRY", Status: "OPEN"}
	l := &pgstore.PositionLeg{
		PositionID: id, LegNo: 1, Phase: "OPEN", Venue: venue,
		Symbol: symbol, Side: side, Qty: 2, PriceExpected: 2400, Status: "FILLED",
	}
	return p, l
}

// TestD015_ExternalCloseMarksLeg #505 形态回放：position_closes 帧到达 →
// ≤2s（同步调用即落）CLOSE 腿 status=EXTERNAL 落库，残腿集合排除该 venue
// → 「券商侧已无此腿」告警根源消失；重帧幂等不重复写。
func TestD015_ExternalCloseMarksLeg(t *testing.T) {
	fs := newFakeStore()
	r := d407Recon(fs, &d407Closer{})
	p, l := d015OpenPos(505, "gate:perp", "XAU_USDT", "SELL")
	fs.positions[505] = p
	fs.legs[505] = []*pgstore.PositionLeg{l}

	px, fee := 2412.5, 0.42
	posID, found, err := r.MarkVenueLegClosed(context.Background(), "gate:perp", "XAU_USDT", "SELL", &px, &fee)
	if err != nil || !found || posID != 505 {
		t.Fatalf("mark: posID=%d found=%v err=%v", posID, found, err)
	}
	var closeLeg *pgstore.PositionLeg
	for _, lg := range fs.legs[505] {
		if lg.Phase == "CLOSE" {
			closeLeg = lg
		}
	}
	if closeLeg == nil {
		t.Fatal("CLOSE 腿未落库")
	}
	if closeLeg.Status != "EXTERNAL" || closeLeg.Side != "BUY" || closeLeg.LegNo != 1 {
		t.Errorf("CLOSE 腿形态错: %+v", closeLeg)
	}
	if closeLeg.PriceActual == nil || *closeLeg.PriceActual != px || closeLeg.FeeUSD == nil || *closeLeg.FeeUSD != fee {
		t.Errorf("帧价格/费用应落库: %+v", closeLeg)
	}
	if v := r.residualLegVenues(context.Background(), 505); len(v) != 0 {
		t.Errorf("EXTERNAL 标记后残腿应空, got %v", v)
	}
	// 幂等：同事件重放不再写。
	if _, _, err := r.MarkVenueLegClosed(context.Background(), "gate:perp", "XAU_USDT", "SELL", &px, &fee); err != nil {
		t.Fatalf("replay: %v", err)
	}
	n := 0
	for _, lg := range fs.legs[505] {
		if lg.Phase == "CLOSE" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("重复帧不得重复落 CLOSE 腿, n=%d", n)
	}
}

// TestD015_ExternalCloseForeignSkip 外单/未知 symbol → found=false 零写入零报错。
func TestD015_ExternalCloseForeignSkip(t *testing.T) {
	fs := newFakeStore()
	r := d407Recon(fs, &d407Closer{})
	p, l := d015OpenPos(505, "gate:perp", "XAU_USDT", "SELL")
	fs.positions[505] = p
	fs.legs[505] = []*pgstore.PositionLeg{l}
	posID, found, err := r.MarkVenueLegClosed(context.Background(), "gate:perp", "BTC_USDT", "SELL", nil, nil)
	if err != nil || found || posID != 0 {
		t.Fatalf("外单应零匹配: posID=%d found=%v err=%v", posID, found, err)
	}
	if len(fs.legs[505]) != 1 {
		t.Error("外单平仓不得写系统腿")
	}
}

// TestD015_DegradedVenueGateSettle D-015 ①：fastRetry 达阈升级 → DegradedVenues 置位；
// ResumeClose 成功 → 清零。
func TestD015_DegradedVenueSettle(t *testing.T) {
	fs := newFakeStore()
	c := &d407Closer{err: errors.New("channel degraded")}
	r := d407Recon(fs, c)
	p, legs := d407ClosingPos(505, time.Minute, "", "gate:perp")
	fs.positions[505] = p
	fs.legs[505] = legs
	for i := 0; i < r.fastRetryConsec; i++ {
		r.fastRetrySweep(context.Background())
	}
	if dv := r.DegradedVenues(); len(dv) != 1 || dv[0] != "gate:perp" {
		t.Fatalf("达阈升级应置降级态, got %v", dv)
	}
	c.err = nil // 通道恢复：下一扫 ResumeClose 成功 → 清零
	r.fastRetrySweep(context.Background())
	if dv := r.DegradedVenues(); len(dv) != 0 {
		t.Errorf("平仓成功应清降级态, got %v", dv)
	}
}

// TestD015_ResidualSkipsCloseFilled ③：同 leg_no 已有 CLOSE FILLED → OPEN FILLED
// 不再计入残腿（external 标记走 EXTERNAL 分支已在上测试覆盖，本测 FILLED 路径）。
func TestD015_ResidualSkipsCloseFilled(t *testing.T) {
	fs := newFakeStore()
	r := d407Recon(fs, &d407Closer{})
	p, l := d015OpenPos(505, "gate:perp", "XAU_USDT", "SELL")
	fs.positions[505] = p
	closeLeg := &pgstore.PositionLeg{
		PositionID: 505, LegNo: 1, Phase: "CLOSE", Venue: "gate:perp",
		Symbol: "XAU_USDT", Side: "BUY", Qty: 2, Status: "FILLED",
	}
	fs.legs[505] = []*pgstore.PositionLeg{l, closeLeg}
	if v := r.residualLegVenues(context.Background(), 505); len(v) != 0 {
		t.Errorf("CLOSE FILLED 已平腿不得计残余, got %v", v)
	}
}
