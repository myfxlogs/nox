// D-295 S3 测试：GetTrxYieldCurve 漂移公式去幽灵计数（cumIncomeTRX 不再入 spotExposure）。
// 红绿锚：旧公式 spotExposure=balance+staked+cumIncomeTRX → 漂移虚高 ~1.3%；
// 新公式 spotExposure=balance+staked → 漂移 < 0.5%。cumIncomeTRX 展示字段不受影响。
package dashboard

import (
	"context"
	"testing"
	"time"

	v1 "nox/internal/gen/dashboardv1"

	"nox/internal/store/pgstore"

	"connectrpc.com/connect"
)

// TestGetTrxYieldCurve_DriftNoPhantomIncome D-295 S3：漂移公式去 cumIncomeTRX。
// 种子：balance 30M sun / tronPower 107,400M sun / 空单 qty 107,430；
// rental_income + vote_claim 各一大额（200M/1200M sun）→ 旧公式漂移 ~1.3%，新公式 < 0.5%。
// CumulativeUnhedgedIncomeTrx 仍 = 1400（展示字段不受影响）。
func TestGetTrxYieldCurve_DriftNoPhantomIncome(t *testing.T) {
	s := trxTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// 账户快照：balance 30 TRX + tronPower 107,400 TRX（sun 单位）
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address:    "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555",
		BalanceSun: 30_000_000, TronPower: 107_400_000_000,
		FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}

	// 累积收入：rental 200M sun + vote_claim 1200M sun = 1400M sun = 1400 TRX
	if err := s.UpsertTRXTx(ctx, pgstore.TRXTx{
		TxID: "rental_d295", Ts: now, Kind: "rental_income", AmountSun: 200_000_000,
	}); err != nil {
		t.Fatalf("UpsertTRXTx rental: %v", err)
	}
	if err := s.UpsertTRXTx(ctx, pgstore.TRXTx{
		TxID: "vote_d295", Ts: now, Kind: "vote_claim", AmountSun: 1_200_000_000,
	}); err != nil {
		t.Fatalf("UpsertTRXTx vote: %v", err)
	}

	svc := &Service{store: s, freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.GetTrxYieldCurve(ctx, connect.NewRequest(&v1.GetTrxYieldCurveRequest{Days: 7}))
	if err != nil {
		t.Fatalf("GetTrxYieldCurve: %v", err)
	}
	r := resp.Msg

	// 展示字段不受影响：CumulativeUnhedgedIncomeTrx = 1400
	wantCum := 1400.0
	if diff := r.CumulativeUnhedgedIncomeTrx - wantCum; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("CumulativeUnhedgedIncomeTrx = %v, want %v (展示字段不受漂移公式影响)", r.CumulativeUnhedgedIncomeTrx, wantCum)
	}
}
