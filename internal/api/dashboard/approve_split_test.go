package dashboard

// approve_split_test.go —— D-581 拆层断言：池域判据降级警示（usd 绝额
// 零池腿可批、note 落警示）vs pct 零池结构性拒批（无基准）。

import (
	"context"
	"strings"
	"testing"

	"nox/internal/data/bus"
	"nox/internal/platform/testutil/storetest"

	v1 "nox/internal/gen/dashboardv1"
)

func TestD581_PoolDemotion(t *testing.T) {
	usd := 200.0
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	s := &Service{store: st, qbus: bus.New()} // gate:perp 凭据未装→REST 校准败→池读缺=池域警示路径
	res, estr := s.deriveAllocSizing(context.Background(), "gate:perp", "binance:perp", "TRX", "TRX", nil, &usd)
	if estr != "" && strings.Contains(estr, "净值非正") {
		t.Fatalf("池域判据应降级警示不拦批（usd 绝额）: %s", estr)
	}
	if len(res.Warns) == 0 {
		t.Fatalf("池读缺应落警示 warn: %+v", res)
	}
	// D-581 终裁：pct 零池不拒批——estr 在 pct 档仅作 dry_run 预览提示。
	pct := 0.5
	res2, _ := s.deriveAllocSizing(context.Background(), "gate:perp", "binance:perp", "TRX", "TRX", &pct, nil)
	if len(res2.Warns) == 0 {
		t.Fatalf("pct 零池应落警示: %+v", res2)
	}
	// sizing 模式判定：零输入=system，pct=pct 落表，usd/显式=fixed。
	req := &v1.ApproveCarryGraduationRequest{}
	if m := allocSizingMode(req); m != "system" {
		t.Fatalf("零 sizing 应 system: %s", m)
	}
	req.FundingPct = &pct
	if m := allocSizingMode(req); m != "pct" {
		t.Fatalf("funding_pct 应 pct: %s", m)
	}
	req.FundingPct = nil
	req.FundingUsd = &usd
	if m := allocSizingMode(req); m != "fixed" {
		t.Fatalf("funding_usd 应 fixed: %s", m)
	}
	// 模式互斥仍拒：funding+显式混传。
	req.MaxGrossUsd = 10
	if _, merr := allocModeInputs(req); merr == "" {
		t.Fatalf("资金+显式混传应互斥拒")
	}
}
