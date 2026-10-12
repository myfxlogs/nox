package engine

import (
	"testing"

	"arb/internal/store/pgstore"
)

// TestNetStoreGate D-085 息差家族净门槛 + D-078 CROSS_VENUE 回归。
// 三型各两用例（净过线入库/净差 0.1 不入库）+ CROSS_VENUE 回归不变。
// 息差型 net 语义=年化百分点；CROSS_VENUE net 语义=一次性 bps（勿混用同一把尺子）。
func TestNetStoreGate(t *testing.T) {
	e := &Engine{
		minStoreNetBps:          5.0,   // D-078 CROSS_VENUE
		fundingHedgeMinNetStore: 14.95, // D-085 含基准 10.95+门槛 4
		perpFundingMinNetStore:  2.0,
		carryMinNetStore:        4.0,
	}

	t.Run("FUNDING_HEDGE 净过线入库（net=15 ≥ 14.95）", func(t *testing.T) {
		o := &pgstore.Opportunity{Type: "FUNDING_HEDGE", NetBps: 15.0}
		if !netStoreGate(o, e) {
			t.Error("net=15 ≥ 14.95 应入库")
		}
	})
	t.Run("FUNDING_HEDGE 净差 0.1 不入库（net=14.85 < 14.95）", func(t *testing.T) {
		o := &pgstore.Opportunity{Type: "FUNDING_HEDGE", NetBps: 14.85}
		if netStoreGate(o, e) {
			t.Error("net=14.85 < 14.95 应不入库")
		}
	})

	t.Run("PERP_FUNDING 净过线入库（net=2.0 ≥ 2.0）", func(t *testing.T) {
		o := &pgstore.Opportunity{Type: "PERP_FUNDING", NetBps: 2.0}
		if !netStoreGate(o, e) {
			t.Error("net=2.0 ≥ 2.0 应入库")
		}
	})
	t.Run("PERP_FUNDING 净差 0.1 不入库（net=1.9 < 2.0）", func(t *testing.T) {
		o := &pgstore.Opportunity{Type: "PERP_FUNDING", NetBps: 1.9}
		if netStoreGate(o, e) {
			t.Error("net=1.9 < 2.0 应不入库")
		}
	})

	t.Run("CARRY 净过线入库（net=4.0 ≥ 4.0）", func(t *testing.T) {
		o := &pgstore.Opportunity{Type: "CARRY", NetBps: 4.0}
		if !netStoreGate(o, e) {
			t.Error("net=4.0 ≥ 4.0 应入库")
		}
	})
	t.Run("CARRY 净差 0.1 不入库（net=3.9 < 4.0）", func(t *testing.T) {
		o := &pgstore.Opportunity{Type: "CARRY", NetBps: 3.9}
		if netStoreGate(o, e) {
			t.Error("net=3.9 < 4.0 应不入库")
		}
	})

	t.Run("CROSS_VENUE 回归不变（net=5 ≥ 5 入 / net=4.9 < 5 不入）", func(t *testing.T) {
		if !netStoreGate(&pgstore.Opportunity{Type: "CROSS_VENUE", NetBps: 5.0}, e) {
			t.Error("CROSS_VENUE net=5 ≥ 5 应入库（D-078 回归）")
		}
		if netStoreGate(&pgstore.Opportunity{Type: "CROSS_VENUE", NetBps: 4.9}, e) {
			t.Error("CROSS_VENUE net=4.9 < 5 应不入库（D-078 回归）")
		}
	})

	t.Run("零值门槛=不过滤（测试默认放行）", func(t *testing.T) {
		zero := &Engine{} // 所有门槛零值
		for _, typ := range []string{"CROSS_VENUE", "FUNDING_HEDGE", "PERP_FUNDING", "CARRY"} {
			o := &pgstore.Opportunity{Type: typ, NetBps: -100}
			if !netStoreGate(o, zero) {
				t.Errorf("type=%s 零值门槛应放行（net=-100）", typ)
			}
		}
	})

	t.Run("未知类型放行（default）", func(t *testing.T) {
		o := &pgstore.Opportunity{Type: "SWAPARB", NetBps: 0.1}
		if !netStoreGate(o, e) {
			t.Error("未知类型应放行（SWAPARB 不受净门槛约束）")
		}
	})
}
