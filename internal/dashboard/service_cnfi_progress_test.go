package dashboard

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	"arb/internal/testutil/storetest"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// P3 四层进度与录入校验（DSN 门控 D-207；金额为合成测试值，非个人数据）。
func cnfiProgressSetup(t *testing.T) *Service {
	t.Helper()
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_holdings WHERE opened_date='2099-01-02'`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_nav WHERE code LIKE 'TEST%'`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_instruments WHERE code LIKE 'TEST%'`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM cnfi_signal_config WHERE key='layer_targets'`)
	})
	// 工具：两只基金 + 净值 + 两笔持仓（流动 4000、票息 6000 → 40%/60%）。
	for _, ins := range []struct{ code, kind, name string }{
		{"TESTF1", "fund", "测试货基"}, {"TESTF2", "fund", "测试中短债"},
	} {
		if err := st.UpsertCNFIInstrument(ctx, ins.code, ins.kind, ins.name, true); err != nil {
			t.Fatalf("instrument: %v", err)
		}
		if err := st.UpsertCNFINAV(ctx, ins.code, "2099-01-02", 1.0, nil, "test", time.Now().UTC()); err != nil {
			t.Fatalf("nav: %v", err)
		}
	}
	day := "2099-01-02"
	if _, err := st.AddCNFIHolding(ctx, "TESTF1", "liquidity", 4000, 4000, day, ""); err != nil {
		t.Fatalf("holding1: %v", err)
	}
	if _, err := st.AddCNFIHolding(ctx, "TESTF2", "coupon", 6000, 6000, day, ""); err != nil {
		t.Fatalf("holding2: %v", err)
	}
	cfg := `{"liquidity":20,"coupon":50,"locked":15,"opportunity":15}`
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO cnfi_signal_config (key,value,updated_by) VALUES ('layer_targets',$1,'test')
		 ON CONFLICT (key) DO UPDATE SET value=$1`, cfg); err != nil {
		t.Fatalf("config: %v", err)
	}
	return &Service{store: st}
}

func TestGetCNFIProgress_Math(t *testing.T) {
	s := cnfiProgressSetup(t)
	resp, err := s.GetCNFIProgress(context.Background(),
		connect.NewRequest(&v1.GetCNFIProgressRequest{}))
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	got := map[string]*v1.CnfiLayerProgress{}
	for _, l := range resp.Msg.Layers {
		got[l.Layer] = l
	}
	if l := got["liquidity"]; l == nil || l.ActualPct != 40.0 || l.TargetPct != 20.0 || l.Holdings != 1 {
		t.Fatalf("liquidity = %+v (want actual 40 target 20 holdings 1)", l)
	}
	if l := got["coupon"]; l == nil || l.ActualPct != 60.0 || l.TargetPct != 50.0 {
		t.Fatalf("coupon = %+v (want actual 60 target 50)", l)
	}
	if l := got["locked"]; l == nil || l.ActualPct != 0.0 || l.TargetPct != 15.0 {
		t.Fatalf("locked = %+v (want actual 0 target 15)", l)
	}
	if resp.Msg.ValuedAsOf != "2099-01-02" {
		t.Fatalf("valued_as_of = %q", resp.Msg.ValuedAsOf)
	}
}

func TestAddCNFIHolding_Validation(t *testing.T) {
	s := cnfiProgressSetup(t)
	ctx := context.Background()
	cases := []struct {
		name   string
		req    *v1.AddCNFIHoldingRequest
		wantOk bool
	}{
		{"非法层", &v1.AddCNFIHoldingRequest{Code: "TESTF1", Layer: "yolo", Qty: 1, CostValue: 1}, false},
		{"负数", &v1.AddCNFIHoldingRequest{Code: "TESTF1", Layer: "coupon", Qty: -1, CostValue: 1}, false},
		{"空代码", &v1.AddCNFIHoldingRequest{Layer: "coupon", Qty: 1, CostValue: 1}, false},
		{"合法", &v1.AddCNFIHoldingRequest{Code: "TESTF1", Layer: "locked", Qty: 100, CostValue: 100, OpenedDate: "2099-01-02"}, true},
	}
	for _, tc := range cases {
		resp, err := s.AddCNFIHolding(ctx, connect.NewRequest(tc.req))
		if err != nil {
			t.Fatalf("%s: rpc error: %v", tc.name, err)
		}
		if resp.Msg.Ok != tc.wantOk {
			t.Fatalf("%s: ok=%v err=%q (want %v)", tc.name, resp.Msg.Ok, resp.Msg.Error, tc.wantOk)
		}
	}
}
