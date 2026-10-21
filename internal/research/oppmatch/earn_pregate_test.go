// earn_pregate_test.go —— earn 前提闸集成锚（返修：真 earnIncrementOK 走
// testStore 非复刻件）：已部署+spot<$10→不产 / spot≥阈→产 / 异所不误伤 /
// stale 行剔出口径 四支。
package oppmatch

import (
	"context"
	"testing"
	"time"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

func snap(venue, vt, ccy string, eq float64, fetchedAt time.Time) pgstore.VenueAccountSnapshot {
	return pgstore.VenueAccountSnapshot{
		Venue: venue, VenueType: vt, Currency: ccy,
		BalanceRaw: eq, EquityRaw: eq, FreeMarginRaw: eq, FetchedAt: fetchedAt,
	}
}

func TestEarnIncrementOK_Integration(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	st, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.InsertVenueAccountSnapshots(ctx, []pgstore.VenueAccountSnapshot{
		// 支①：已部署（earn $2128）+spot $5.2<阈 → 不产
		snap("zzpreA", "spot", "USDT", 5.2, now),
		snap("zzpreA", "earn", "USDT", 2128, now),
		// 支②：earn $2000 在息 + spot $500 真增量 → 产
		snap("zzpreB", "spot", "USDT", 500, now),
		snap("zzpreB", "earn", "USDT", 2000, now),
		// 支④：spot $500 但快照陈旧（事件源窗 5min→stale 剔；src 显式钉
		// 非 scheduler——空 src 落 scheduler 36h 窗不判陈）。
		func() pgstore.VenueAccountSnapshot {
			s := snap("zzpreD", "spot", "USDT", 500, now.Add(-10*time.Minute))
			s.Src = "rest_poll"
			return s
		}(),
	}); err != nil {
		t.Fatal(err)
	}

	if ok, why := earnIncrementOK(ctx, st, "zzpreA"); ok {
		t.Fatalf("已部署+spot≈0 应不产（得 ok，why=%s）", why)
	}
	if ok, why := earnIncrementOK(ctx, st, "zzpreB"); !ok {
		t.Fatalf("真增量应产（得拒 %s）", why)
	}
	// 支③：异所不误伤——B 过闸不救 A，A 拒不误伤 B（上行两断已并证）。
	if ok, _ := earnIncrementOK(ctx, st, "zzpreD"); ok {
		t.Fatal("stale 池行应剔出 spotFree 口径（陈旧值不当现值）")
	}
}
