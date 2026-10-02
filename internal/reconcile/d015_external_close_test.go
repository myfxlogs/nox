// d015_external_close_test.go —— D-015 ①③ 验收（R1）。
// R1 修订：venue 平仓/强平/ADL 帧只 recon.Kick 不落库——EXTERNAL 写入收敛到
// pipeline_resume_helpers 历史回填无命中的 cex_private_events 兜底（exec 侧测试
// 覆盖），本文件锚 reconciler 保留面：降级态集合观测（不入 Gate）+ 残腿排除。
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

// TestD015_DegradedVenueGateSettle D-015 ①：fastRetry 达阈升级 → DegradedVenues 置位；
// ResumeClose 成功 → 清零。R1：集合仅观测/告警，不入 Gate 开仓判定。
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
// 不再计入残腿。
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

// TestD015_ResidualSkipsExternal ③：同 leg_no 已有 CLOSE EXTERNAL（历史回填兜底
// 写入）→ 残腿同样排除——EXTERNAL 行=venue 平仓帧证据链认定的「已平」事实。
func TestD015_ResidualSkipsExternal(t *testing.T) {
	fs := newFakeStore()
	r := d407Recon(fs, &d407Closer{})
	p, l := d015OpenPos(505, "gate:perp", "XAU_USDT", "SELL")
	fs.positions[505] = p
	closeLeg := &pgstore.PositionLeg{
		PositionID: 505, LegNo: 1, Phase: "CLOSE", Venue: "gate:perp",
		Symbol: "XAU_USDT", Side: "BUY", Qty: 2, Status: "EXTERNAL",
	}
	fs.legs[505] = []*pgstore.PositionLeg{l, closeLeg}
	if v := r.residualLegVenues(context.Background(), 505); len(v) != 0 {
		t.Errorf("CLOSE EXTERNAL 已平腿不得计残余, got %v", v)
	}
}
