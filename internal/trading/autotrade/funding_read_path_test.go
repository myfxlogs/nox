package autotrade

import (
	"context"
	"testing"
	"time"

	"nox/internal/platform/testutil/storetest"
)

// TestFundingReadPath 对抗锚：facts 有据时引擎读径必中（「数据在而读不到」
// 回归锁——facts binance|TRXUSDT|funding 在库而引擎 missing 的错位实证）。
func TestFundingReadPath(t *testing.T) {
	s := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM facts WHERE symbol='TFRP%'`,
	} {
		if _, err := s.Pool().Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO facts (kind,venue,symbol,value,unit,ts,src) VALUES
		 ('funding','binance','TFRPUSDT',-1.2,'pct_annual',now(),'t'),
		 ('funding','binance','TFRPUSDT',-1.3,'pct_annual',now()-interval '2 days','t')`); err != nil {
		t.Fatal(err)
	}
	defer s.Pool().Exec(ctx, `DELETE FROM facts WHERE symbol='TFRP%'`)
	e := &Engine{store: s}
	// window>0 窗均径（生产主径——facts 有据必 ok=true）
	// facts 有据必 ok=true（对抗锚=错位实证「数据在而读不到」回归锁；
	// rate 符号=种子值如实——SELL 收负率=实付不违）。
	rate, ok := e.xassetLegCarryAnnual(ctx, "binance:perp", "TFRPUSDT", "SELL", time.Hour, carryEconWindow)
	if !ok || rate == 0 {
		t.Fatalf("facts 有据时应 ok=true 且 rate 非零: rate=%.4f ok=%v", rate, ok)
	}
	// window<=0 瞬时径同检
	rate, ok = e.xassetLegCarryAnnual(ctx, "binance:perp", "TFRPUSDT", "SELL", time.Hour, 0)
	if !ok || rate == 0 {
		t.Fatalf("瞬时径应 ok=true 且 rate 非零: rate=%.4f ok=%v", rate, ok)
	}
	// 真缺行仍 fail-closed（禁线语义保）
	if _, ok := e.xassetLegCarryAnnual(ctx, "binance:perp", "NOPEUSDT", "SELL", time.Hour, carryEconWindow); ok {
		t.Fatal("真缺行应 fail-closed ok=false")
	}
}
