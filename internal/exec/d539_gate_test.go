// d539_gate_test.go —— D-539：平仓执行核心全腿前置闸（防休市窗单腿裸露）。
// 主锚：CLOSING 仓一腿暗 → ResumeClose 整仓零发单（亮腿也不发）+ ErrLegNotActionable
// + 仓保持 CLOSING + note await_session 透出。回归锚：全腿亮 → 全平 CLOSED 语义不变。
// force=true 逃逸锚复用 TestD495_CloseForce_BypassesGate（同走 core force 路径）。
package exec

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

func d539Pipeline(s *pgstore.Store, placed, mtClosed *int32, orders map[string][]OpenedOrder, drainAfter int) *Pipeline {
	reg := NewRegistry()
	reg.Register(&fakeFlattenExecutor{venue: "okx:perp", placeCount: placed})
	reg.Register(&fakeMT5CloseExecutor{venue: "3001157:mt5", placeCount: mtClosed})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	p.verifier = &fakeVerifier{drainAfter: drainAfter, orders: orders}
	return p
}

// 主锚（先红后绿）：CLOSING 仓双腿 FILLED、okx 亮 mt 暗（ZZZUSDm 不在共享播种表
// 且无 canonical 映射=真暗）→ ResumeClose 整仓零发单
// （亮腿也不发——pos502 形态：旧语义先平亮腿、暗腿裸露一周末）。
func TestD539_ResumeClose_DarkLegBlocksAll(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	posID := d495SeedPos(t, s, "CLOSING")
	d495SeedLeg(t, s, posID, 0, "okx:perp", "XAU-USDT-SWAP", "SELL", "cex-open-g1")
	d495SeedLeg(t, s, posID, 1, "3001157:mt5", "ZZZUSDm", "BUY", "mt-ticket-g1")

	var placed, mtClosed int32
	p := d539Pipeline(s, &placed, &mtClosed, map[string][]OpenedOrder{
		"okx:perp":    {{Symbol: "XAU-USDT-SWAP", Side: "SELL", PosSide: "short"}},
		"3001157:mt5": {{Ticket: "mt-ticket-g1"}},
	}, 0)

	err := p.ResumeClose(ctx, posID)
	if !errors.Is(err, ErrLegNotActionable) {
		t.Fatalf("暗腿仓 ResumeClose 应 ErrLegNotActionable, got %v", err)
	}
	if atomic.LoadInt32(&placed) != 0 {
		t.Fatal("前置闸命中后亮腿也不得发平仓单")
	}
	if atomic.LoadInt32(&mtClosed) != 0 {
		t.Fatal("前置闸命中后暗腿不得有平仓调用")
	}
	closeLegs, _ := s.ListPositionLegsByPhase(ctx, posID, "CLOSE")
	if len(closeLegs) != 0 {
		t.Fatalf("闸挡轮不得写 CLOSE leg, got %d", len(closeLegs))
	}
	pos, _ := s.GetPosition(ctx, posID)
	if pos.Status != "CLOSING" {
		t.Fatalf("仓应保持 CLOSING 等下轮, got %s", pos.Status)
	}
	if !strings.Contains(pos.Note, "await_session:3001157:mt5:ZZZUSDm") {
		t.Fatalf("note 应透出暗腿 await_session token: %q", pos.Note)
	}
	// 幂等：二次 ResumeClose 同结果，note 不漂移。
	if err := p.ResumeClose(ctx, posID); !errors.Is(err, ErrLegNotActionable) {
		t.Fatalf("二次 ResumeClose 应仍 ErrLegNotActionable, got %v", err)
	}
	pos2, _ := s.GetPosition(ctx, posID)
	if pos2.Note != pos.Note {
		t.Fatalf("note 幂等重写不得漂移: %q → %q", pos.Note, pos2.Note)
	}
}

// 回归锚：双腿全亮（共享播种表已覆盖 XAU-USDT-SWAP@okx 与 XAUUSDm@3001157）→
// 前置闸放行，逐腿发单全平 CLOSED（旧语义不破）。
func TestD539_ResumeClose_AllBright_ClosesAll(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	posID := d495SeedPos(t, s, "CLOSING")
	d495SeedLeg(t, s, posID, 0, "okx:perp", "XAU-USDT-SWAP", "SELL", "cex-open-g2")
	d495SeedLeg(t, s, posID, 1, "3001157:mt5", "XAUUSDm", "BUY", "mt-ticket-g2")

	var placed, mtClosed int32
	p := d539Pipeline(s, &placed, &mtClosed, map[string][]OpenedOrder{
		"okx:perp":    {{Symbol: "XAU-USDT-SWAP", Side: "SELL", PosSide: "short"}},
		"3001157:mt5": {{Ticket: "mt-ticket-g2"}},
	}, 1) // drainAfter=1：preflight 报在仓、残核查报已平

	if err := p.ResumeClose(ctx, posID); err != nil {
		t.Fatalf("全腿亮应正常平仓, got %v", err)
	}
	if atomic.LoadInt32(&placed) != 1 || atomic.LoadInt32(&mtClosed) != 1 {
		t.Fatalf("两腿应各发 1 平仓单, got okx=%d mt=%d", placed, mtClosed)
	}
	pos, _ := s.GetPosition(ctx, posID)
	if pos.Status != "CLOSED" {
		t.Fatalf("全平后应 CLOSED, got %s", pos.Status)
	}
}
