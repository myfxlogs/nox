package dashboard

import (
	"strings"
	"testing"

	"arb/internal/exec"
)

// T1：qty=0 → 拒因含「最小步长/notional」字样（D-433 显式化）。
func TestD433_T1_RejectReasonExplicit(t *testing.T) {
	s := &Service{}
	err := s.validateLegQtys([]exec.LegSpec{
		{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "SELL", Qty: 0},
	})
	if err == nil {
		t.Fatal("qty=0 未拒")
	}
	if !strings.Contains(err.Error(), "最小步长") || !strings.Contains(err.Error(), "notional") {
		t.Fatalf("拒因不含归因提示: %v", err)
	}
}

// T2：qty=0（上游产出即 0）→ 仍拒（语义不退化）。
func TestD433_T2_ZeroStillRejected(t *testing.T) {
	s := &Service{}
	if err := s.validateLegQtys([]exec.LegSpec{
		{VenueKey: "okx:perp", VenueSymbol: "ZZZ-USDT-SWAP", Side: "SELL", Qty: 0},
	}); err == nil {
		t.Fatal("qty=0 未拒")
	}
}

// T3：qty>0 → 放行（回归锚）。
func TestD433_T3_PositivePasses(t *testing.T) {
	s := &Service{}
	if err := s.validateLegQtys([]exec.LegSpec{
		{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "SELL", Qty: 1},
		{VenueKey: "3001157:mt5", VenueSymbol: "BTCUSDTm", Side: "BUY", Qty: 0.01},
	}); err != nil {
		t.Fatalf("qty>0 被误拒: %v", err)
	}
}
