// d093_exit_fee_test.go —— D-093 锚：平仓净估含出场费（净到手口径）。
// 两腿仓 okx 计提/MT 零 + 缺费率键 warn-once + 坏值零；既有 net 锚（d375 等）腿无
// PriceActual 且 secRes 未装配 → 费路径自动零，零迁移绿。
package dashboard

import (
	"bytes"
	"context"
	"log"
	"math"
	"os"
	"testing"
	"time"

	"arb/internal/config"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func d093Pb(id int64) *v1.Position {
	return &v1.Position{
		Id: id, OpportunityId: "XASSET_CARRY:XAUUSD:277842155:mt5:okx:perp",
		Type: "XASSET_CARRY", Status: "OPEN",
		OpenedAt: timestamppb.New(time.Now().AddDate(0, 0, -3)),
	}
}

// TestD093_ExitFeeOkxLegCharged 两腿仓（okx+MT）：okx 腿 $4149 入口实价 × 5bps = $2.0745
// 计提 → ExitFeeEstUsd=2.0745；net = 旧算式 − 2.0745；MT 腿零费项。
func TestD093_ExitFeeOkxLegCharged(t *testing.T) {
	okxPx := 4149.0
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "okx:perp", Symbol: "XAU-USDT-SWAP", Status: "FILLED", Qty: 1, PriceActual: &okxPx, Ts: time.Now()},
		{Phase: "OPEN", Venue: "277842155:mt5", Symbol: "XAUUSD", Status: "FILLED", Qty: 1, Ts: time.Now()},
	}
	bpm := fakeBPMWithCache([]*v1.BrokerPosition{
		{VenueKey: "okx:perp", Symbol: "XAU-USDT-SWAP", Pnl: 2.0, PositionId: 509},
		{VenueKey: "277842155:mt5", Symbol: "XAUUSD", Pnl: 1.0, PositionId: 509},
	})
	s := newEnrichService(bpm)
	s.SetSecretResolver(config.NewSecretResolver(map[string]string{"ARB_EXIT_FEE_BPS_OKX": "5"}))
	pb := d093Pb(509)
	enrichOne(context.Background(), s, pb, legs, bpm.Snapshot(), time.Now(), map[string]time.Weekday{}, map[string]float64{}, map[int64]float64{})

	if pb.ExitFeeEstUsd == nil || math.Abs(*pb.ExitFeeEstUsd-2.0745) > 1e-9 {
		t.Fatalf("exit_fee_est_usd = %v, want 2.0745（4149×5bps，MT 腿零）", pb.ExitFeeEstUsd)
	}
	// 旧算式 net = floating(3.0) + accrued(0) + commission(0) = 3.0 → 新 net = 3.0 − 2.0745。
	if pb.NetPnlUsd == nil || math.Abs(*pb.NetPnlUsd-0.9255) > 1e-9 {
		t.Fatalf("net = %v, want 0.9255（旧算式 3.0 − 出场费 2.0745）", pb.NetPnlUsd)
	}
}

// TestD093_ExitFeeMissingKeyWarnOnce venue 无费率键 → 费 0（net 不减）+ log-once warn。
func TestD093_ExitFeeMissingKeyWarnOnce(t *testing.T) {
	okxPx := 4149.0
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Venue: "gate:perp", Symbol: "XAU_USDT", Status: "FILLED", Qty: 1, PriceActual: &okxPx, Ts: time.Now()},
	}
	bpm := fakeBPMWithCache([]*v1.BrokerPosition{
		{VenueKey: "gate:perp", Symbol: "XAU_USDT", Pnl: 2.0, PositionId: 510},
	})
	s := newEnrichService(bpm) // secRes 未装配=键缺（warn 路径）
	pb := d093Pb(510)

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	enrichOne(context.Background(), s, pb, legs, bpm.Snapshot(), time.Now(), map[string]time.Weekday{}, map[string]float64{}, map[int64]float64{})

	if pb.ExitFeeEstUsd != nil {
		t.Fatalf("未配置 venue 费应 0（缺省不发）, got %v", *pb.ExitFeeEstUsd)
	}
	if pb.NetPnlUsd == nil || math.Abs(*pb.NetPnlUsd-2.0) > 1e-9 {
		t.Fatalf("net = %v, want 2.0（费 0 不减）", pb.NetPnlUsd)
	}
	if !bytes.Contains(buf.Bytes(), []byte("exit fee 未配置 venue=gate:perp")) {
		t.Fatalf("应打 log-once warn, log=%q", buf.String())
	}
}
