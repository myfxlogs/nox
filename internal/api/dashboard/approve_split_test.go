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
	pct := 0.5
	_, estr = s.deriveAllocSizing(context.Background(), "gate:perp", "binance:perp", "TRX", "TRX", &pct, nil)
	if estr == "" || strings.Contains(estr, "每手名义") {
		t.Fatalf("pct 零基准应结构性拒批: %q", estr)
	}
	_ = v1.ApproveCarryGraduationRequest{}
}
