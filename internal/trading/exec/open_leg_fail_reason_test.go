package exec

// d208_test.go —— D-208 锚：FAILED 腿 fail_reason 落 venue 原文+败腿尝试落行+force 留裸 warn。

import (
	"context"
	"strings"
	"testing"

	"arb/internal/platform/testutil"
	"arb/internal/store/pgstore"
)

// ①OPEN 腿 FAILED → fail_reason 落 venue 拒因原文（≤512B 截尾不截头）。
func TestD208_OpenLegFailReason(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	conn := seedConn(t, testutil.TestDSN(t, "ARB_EXEC_TEST_PG_DSN"))
	var posID int64
	if err := conn.QueryRow(ctx, `INSERT INTO positions (opportunity_id, type, status) VALUES ('opp-d208','CROSS_VENUE','OPEN') RETURNING id`).Scan(&posID); err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{store: s}
	long := "venue reject: margin check failed " + strings.Repeat("x", 600)
	legs := []LegSpec{
		{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 1, PriceExpected: 50000},
		{VenueKey: "3001157:mt5", VenueSymbol: "XAUUSDm", Side: "SELL", Qty: 0.01, PriceExpected: 4000},
	}
	results := []LegResult{
		{VenueKey: "okx:perp", Status: "FAILED", Reason: "binance margin insufficient -2019"},
		{VenueKey: "3001157:mt5", Status: "FAILED", Reason: long},
	}
	if err := p.persistOpenLegs(ctx, "req-d208", posID, legs, results); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListPositionLegs(ctx, posID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("n=%d", len(got))
	}
	if got[0].FailReason == nil || *got[0].FailReason != "binance margin insufficient -2019" {
		t.Fatalf("leg0 fail_reason 原文对拍败: %+v", got[0].FailReason)
	}
	if got[1].FailReason == nil || len(*got[1].FailReason) != 512 ||
		!strings.HasPrefix(*got[1].FailReason, "venue reject: margin check failed") {
		t.Fatalf("leg1 截断对拍败（前缀须保）: len=%v", len(*got[1].FailReason))
	}
	// FILLED 腿 fail_reason=NULL 诚实缺省由 scan 覆盖面（FailReason *string）保证。
}

// ①腿级平仓尝试失败落 FAILED 行 attempt_no 递增 + fail_reason。
func TestD208_FailedCloseAttemptRow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	conn := seedConn(t, testutil.TestDSN(t, "ARB_EXEC_TEST_PG_DSN"))
	var posID int64
	if err := conn.QueryRow(ctx, `INSERT INTO positions (opportunity_id, type, status) VALUES ('opp-d208b','CROSS_VENUE','CLOSING') RETURNING id`).Scan(&posID); err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{store: s}
	openLeg := pgstore.PositionLeg{PositionID: posID, LegNo: 0, Phase: "OPEN",
		Venue: "okx:perp", Symbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 10, PriceExpected: 1, Status: "FILLED"}
	if err := s.CreatePositionLeg(ctx, &openLeg); err != nil {
		t.Fatal(err)
	}
	legs, _ := s.ListPositionLegs(ctx, posID)
	p.persistFailedCloseLeg(ctx, legs, openLeg, errString("venue close rejected: 51008"))
	p.persistFailedCloseLeg(ctx, append(legs, pgstore.PositionLeg{Phase: "CLOSE", LegNo: 0, AttemptNo: 1, Status: "FAILED"}), openLeg, errString("venue close rejected: 51008 again"))
	closes, err := s.ListPositionLegsByPhase(ctx, posID, "CLOSE")
	if err != nil {
		t.Fatal(err)
	}
	if len(closes) != 2 || closes[0].AttemptNo != 1 || closes[1].AttemptNo != 2 {
		t.Fatalf("attempt_no 递增对拍败: %+v", closes)
	}
	for _, c := range closes {
		if c.Status != "FAILED" || c.FailReason == nil || !strings.HasPrefix(*c.FailReason, "venue close rejected") || c.SubmittedAt == nil {
			t.Fatalf("败腿行字段对拍败: %+v", c)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
