// d015_ws_frame_race_test.go —— D-015 R1 验收：「自发平仓 WS 帧先到」竞态 + EXTERNAL 兜底。
// 核心不变量：venue 平仓帧只叫醒对账，不直写 CLOSE 腿——自发平仓走正常管线落唯一
// FILLED 权威腿（价费非空、realized 含腿）；只有历史查证+平仓帧双证据时才写 EXTERNAL。
// 需要 test-pg2 DSN（NOX_TEST_PG_DSN）。
package exec

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
)

// d015VenueCase 三所平仓帧形态（cex_private_events 落库原文 schema）。
type d015VenueCase struct {
	venue   string
	symbol  string
	posSide string // verifier PosSide（long/short）
	channel string
	payload string // 平仓帧原文（position 消失证据）
}

var d015VenueCases = []d015VenueCase{
	{"gate:perp", "BTC_USDT", "long", "futures.position_closes",
		`[{"contract":"BTC_USDT","side":"long","pnl":"0.5","price":"100"}]`},
	{"okx:perp", "BTC-USDT-SWAP", "long", "positions",
		`{"arg":{"channel":"positions"},"data":[{"instId":"BTC-USDT-SWAP","pos":"0","posSide":"long"}]}`},
	{"binance:perp", "BTCUSDT", "long", "account",
		`{"B":[],"P":[{"s":"BTCUSDT","pa":"0","ps":"LONG"}]}`},
}

// seedD015Pos 开仓 OPEN 仓 + FILLED 腿（PriceActual=90 供 realized 断言）。
func seedD015Pos(t *testing.T, ctx context.Context, s *pgstore.Store, c d015VenueCase) int64 {
	t.Helper()
	var posID int64
	if err := s.Pool().QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO positions (opportunity_id, type, status, opened_at)
		VALUES ('PERP:BTCUSD:%s::d015', 'PERP_FUNDING', 'OPEN', NOW())
		RETURNING id`, c.venue)).Scan(&posID); err != nil {
		t.Fatalf("seed position %s: %v", c.venue, err)
	}
	openPx, openFee := 90.0, 0.0
	if err := s.CreatePositionLeg(ctx, &pgstore.PositionLeg{
		PositionID: posID, LegNo: 0, Phase: "OPEN", Venue: c.venue,
		Symbol: c.symbol, Side: "BUY", Qty: 0.5, PriceExpected: 90, PriceActual: &openPx,
		OrderID: "T-d015-open", Status: "FILLED", AvgFillPrice: &openPx, FeeUSD: &openFee,
	}); err != nil {
		t.Fatalf("seed leg %s: %v", c.venue, err)
	}
	return posID
}

// TestD015_WSFrameBeforeFill 三所回放：venue 平仓帧先于平仓回报入 cex_private_events
// （WS 帧竞态先到），系统自发平仓仍落唯一 CLOSE FILLED 腿——帧不产生 EXTERNAL 行、
// 不抢 attempt 序位；价/费非空、realized_pnl 含该腿。
func TestD015_WSFrameBeforeFill(t *testing.T) {
	for _, c := range d015VenueCases {
		t.Run(c.venue, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows, cex_private_events RESTART IDENTITY CASCADE`); err != nil {
				t.Fatalf("truncate: %v", err)
			}
			posID := seedD015Pos(t, ctx, s, c)
			// WS 平仓帧先到（对账尚未跑）——只应留审计行，不产生任何 CLOSE 腿。
			bare, vtype := venueParts(c.venue)
			if err := s.InsertCEXPrivateEvent(ctx, bare, vtype, c.channel, "", []byte(c.payload)); err != nil {
				t.Fatalf("insert frame %s: %v", c.venue, err)
			}
			reg := NewRegistry()
			reg.Register(&fakeFlattenExecutor{venue: c.venue, feeUSD: 0.5})
			p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
			// 平仓单发出前腿仍在仓；residual 复核时 venue 已无腿（正常平仓完成态）。
			p.verifier = &fakeVerifier{drainAfter: 1, orders: map[string][]OpenedOrder{
				c.venue: {{Symbol: c.symbol, Side: "BUY", PosSide: c.posSide}},
			}}
			if err := p.ClosePosition(ctx, posID); err != nil {
				t.Fatalf("ClosePosition %s: %v", c.venue, err)
			}
			closes, err := s.ListPositionLegsByPhase(ctx, posID, "CLOSE")
			if err != nil {
				t.Fatalf("list close legs: %v", err)
			}
			if len(closes) != 1 {
				t.Fatalf("CLOSE 腿唯一性, got %d", len(closes))
			}
			cl := closes[0]
			if cl.Status != "FILLED" {
				t.Fatalf("WS 帧先到不得抢占 EXTERNAL：CLOSE status=%s", cl.Status)
			}
			if cl.PriceActual == nil || *cl.PriceActual <= 0 {
				t.Errorf("CLOSE price_actual 缺：%+v", cl)
			}
			if cl.FeeUSD == nil {
				t.Errorf("CLOSE fee_usd 缺：%+v", cl)
			}
			pos, err := s.GetPosition(ctx, posID)
			if err != nil {
				t.Fatalf("get pos: %v", err)
			}
			if pos.Status != "CLOSED" {
				t.Fatalf("pos status=%s want CLOSED", pos.Status)
			}
			// realized 含腿：(100-90)×0.5 毛值 − 0.5 平仓费 = 4.5 净额。
			if pos.RealizedPnl == nil || math.Abs(*pos.RealizedPnl-4.5) > 1e-6 {
				t.Errorf("realized_pnl 未含腿: %v（want ≈4.5）", pos.RealizedPnl)
			}
		})
	}
}

// TestD015_ExternalCloseFallback 外部平仓兜底：venue 已无腿 + 历史查证无命中 +
// cex_private_events 平仓帧命中 → CLOSE EXTERNAL 落库收尾（无「已无此腿」critical）。
func TestD015_ExternalCloseFallback(t *testing.T) {
	for _, c := range d015VenueCases {
		t.Run(c.venue, func(t *testing.T) {
			s := testStore(t)
			ctx := context.Background()
			if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows, cex_private_events RESTART IDENTITY CASCADE`); err != nil {
				t.Fatalf("truncate: %v", err)
			}
			posID := seedD015Pos(t, ctx, s, c)
			bare, vtype := venueParts(c.venue)
			if err := s.InsertCEXPrivateEvent(ctx, bare, vtype, c.channel, "", []byte(c.payload)); err != nil {
				t.Fatalf("insert frame %s: %v", c.venue, err)
			}
			reg := NewRegistry()
			reg.Register(&fakeNoOrderExecutor{venue: c.venue})
			p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
			p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{c.venue: {}}} // venue 已无腿
			p.SetPnLCalibrator(&fakePnLCalibrator{results: map[string]*HistoryPnL{}}) // 历史无命中
			if err := p.ClosePosition(ctx, posID); err != nil {
				t.Fatalf("ClosePosition %s: %v", c.venue, err)
			}
			closes, err := s.ListPositionLegsByPhase(ctx, posID, "CLOSE")
			if err != nil {
				t.Fatalf("list close legs: %v", err)
			}
			if len(closes) != 1 || closes[0].Status != "EXTERNAL" {
				t.Fatalf("平仓帧命中应写唯一 EXTERNAL 腿, got %+v", closes)
			}
			pos, err := s.GetPosition(ctx, posID)
			if err != nil {
				t.Fatalf("get pos: %v", err)
			}
			if pos.Status != "CLOSED" {
				t.Fatalf("pos status=%s want CLOSED", pos.Status)
			}
		})
	}
}

// TestD015_ExternalCloseFallbackMiss 平仓帧也无命中 → fail-closed critical 保留。
func TestD015_ExternalCloseFallbackMiss(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows, cex_private_events RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	posID := seedD015Pos(t, ctx, s, d015VenueCases[1]) // okx
	reg := NewRegistry()
	reg.Register(&fakeNoOrderExecutor{venue: "okx:perp"})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{"okx:perp": {}}}
	p.SetPnLCalibrator(&fakePnLCalibrator{results: map[string]*HistoryPnL{}})
	if err := p.ClosePosition(ctx, posID); err == nil {
		t.Fatal("帧+历史双无命中应 fail-closed 返回 error")
	}
	closes, _ := s.ListPositionLegsByPhase(ctx, posID, "CLOSE")
	if len(closes) != 0 {
		t.Fatalf("无命中不得写 CLOSE 腿, got %+v", closes)
	}
}

// venueParts "okx:perp" → ("okx","perp")（keydomain 同义，测试本地免引）。
func venueParts(vk string) (bare, vtype string) {
	if i := strings.IndexByte(vk, ':'); i >= 0 {
		return vk[:i], vk[i+1:]
	}
	return vk, ""
}
