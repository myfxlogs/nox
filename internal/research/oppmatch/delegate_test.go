package oppmatch

import (
	"context"
	"testing"
	"time"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

// TestD513_DelegateGraduation：委任全条件达标→自动批（approved+decided_by+combo+warn 告警）；
// 各条件缺一的反例行留 pending；日批≤1 闸钉死。
func TestD513_DelegateGraduation(t *testing.T) {
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
	// 共享库隔离：委任批只认本票符号域——清残留 grad/paper/告警基线。
	for _, q := range []string{
		`DELETE FROM carry_graduations WHERE symbol LIKE 'DELGT%'`,
		`DELETE FROM paper_spot_carry WHERE symbol LIKE 'DELGT%'`,
		`DELETE FROM xasset_allow_combos WHERE canonical_buy LIKE 'DELGT%'`,
		`DELETE FROM alerts WHERE message LIKE '%毕业委任%'`,
		`DELETE FROM app_config WHERE key='grad_delegate_enabled'`,
	} {
		if _, err := st.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	// live 白名单域：DELGTBTC 在册（品种限既有 live 模板域）。
	if _, err := st.InsertXAssetAllowCombo(ctx, &pgstore.XAssetComboInsert{
		CanonicalBuy: "DELGTEXIST", CanonicalSell: "DELGTEXIST",
		VenueBuy: "gate:spot", VenueSell: "gate:perp", // 异所 seed——live canonical 域供查，委任写 binance 不撞唯一键
		MaxLongQty: 1, MaxShortQty: 1, MaxGrossUSD: 100, Simulated: false, Note: "delegate live seed",
	}); err != nil {
		t.Fatal(err)
	}
	// 现价 tick（qty=QtyUSD/px 推导源；ticks 月分区惰性建）。
	if err := st.EnsureMonthPartition(ctx, time.Now().Year(), int(time.Now().Month())); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertTicks(ctx, []pgstore.TickRow{
		{Ts: time.Now(), Venue: "binance", Symbol: "DELGTEXIST", Bid: 100, Ask: 100.1, VenueType: "spot"},
	}); err != nil {
		t.Fatal(err)
	}
	reloaded := 0
	GradDelegateReload = func(context.Context) error { reloaded++; return nil }
	defer func() { GradDelegateReload = nil }()

	mk := func(sym string, paperAgeH float64, accrued, qtyUSD float64, flagWarn *string, gradAgeH float64, structure string) int64 {
		p := &pgstore.PaperSpotCarry{VenueA: "binance", VenueB: "binance", VenueTypeA: "spot", VenueTypeB: "perp",
			Symbol: sym, Side: "spot_carry", EntryFundingAnn: 10, QtyUSD: qtyUSD, Structure: structure}
		if err := st.InsertPaperSpotCarry(ctx, p); err != nil {
			t.Fatal(err)
		}
		// 同 symbol 多纸面行撞 active 唯一索引——翻 exited 绕开（委任判纸面证据不判状态）。
		if _, err := st.Pool().Exec(ctx,
			`UPDATE paper_spot_carry SET status='exited', entry_ts=now()-$2::interval, accrued_usd=$3 WHERE id=$1`,
			p.ID, time.Duration(paperAgeH*float64(time.Hour)).String(), accrued); err != nil {
			t.Fatal(err)
		}
		g := &pgstore.CarryGraduation{PaperID: p.ID, VenueA: "binance", VenueB: "", Symbol: sym,
			Structure: structure, AgeDays: paperAgeH / 24, AccruedUSD: accrued, FlagWarn: flagWarn}
		if _, err := st.InsertCarryGraduation(ctx, g); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool().Exec(ctx,
			`UPDATE carry_graduations SET created_at=now()-$2::interval WHERE id=$1`,
			g.ID, time.Duration(gradAgeH*float64(time.Hour)).String()); err != nil {
			t.Fatal(err)
		}
		return g.ID
	}

	warn := "flag hit"
	idOK := mk("DELGTEXIST", 400, 12, 300, nil, 100, "spot_carry")           // 全条件达标
	idYoung := mk("DELGTEXIST", 100, 12, 300, nil, 100, "spot_carry")        // paper 龄<14d
	idFlag := mk("DELGTEXIST", 400, 12, 300, &warn, 100, "spot_carry")       // flag_warn 非空
	idOver := mk("DELGTEXIST", 400, 12, 800, nil, 100, "spot_carry")         // 额度超帽
	idCfd := mk("DELGTEXIST", 400, 12, 300, nil, 100, "long_cfd_short_perp") // 结构域外
	idFresh := mk("DELGTEXIST", 400, 12, 300, nil, 50, "spot_carry")         // pending<3d
	idNoLive := mk("DELGTNO", 400, 12, 300, nil, 100, "spot_carry")          // 品种非 live 域

	delegateGraduations(ctx, st)

	got, err := st.GetCarryGraduation(ctx, idOK)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "approved" || got.DecidedBy != "delegate:D-513" || got.ComboID == nil {
		t.Fatalf("达标行应委任批+combo：%+v", got)
	}
	for _, id := range []int64{idYoung, idFlag, idOver, idCfd, idFresh, idNoLive} {
		g2, _ := st.GetCarryGraduation(ctx, id)
		if g2.Status != "pending" {
			t.Fatalf("反例行 %d 应留 pending，得 %s", id, g2.Status)
		}
	}
	if reloaded != 1 {
		t.Fatalf("白名单热载应调 1 次，得 %d", reloaded)
	}
	// warn 告警逐笔落。
	var warns int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE message LIKE '%毕业委任%' AND level='warn'`).Scan(&warns); err != nil {
		t.Fatal(err)
	}
	if warns != 1 {
		t.Fatalf("委任 warn 告警应 1 条，得 %d", warns)
	}
	// 日批≤1：放行第二达标行仍不批（当日额度耗尽）。
	mk("DELGTEXIST", 500, 20, 200, nil, 200, "spot_carry")
	delegateGraduations(ctx, st)
	var approved int
	if err := st.Pool().QueryRow(ctx,
		`SELECT count(*) FROM carry_graduations WHERE decided_by='delegate:D-513'`).Scan(&approved); err != nil {
		t.Fatal(err)
	}
	if approved != 1 {
		t.Fatalf("日批≤1 闸：当日委任批应 1，得 %d", approved)
	}
}
