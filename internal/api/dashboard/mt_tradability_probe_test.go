// mt_tradability_probe_test.go —— MT5 符号可交易性只读探测（D-051 CFD 闭环辅助）。
// 仅当 NOX_DEMO_ORDER=1 且 NOX_DEMO_PG_DSN 设置时运行；对每个 MT5 venue 连适配器查
// SymbolParams.SymGroup.TradeMode（MT5 SYMBOL_TRADE_MODE：0=DISABLED,1=LONGONLY,
// 2=SHORTONLY,3=CLOSEONLY,4=FULLACCESS——D-060 F6 修为全枚举），只读不下单——
// 用于确认哪个 venue 的 BTC 符号真正可双向交易
// （1314615 BTCUSD 曾报 "Trading is disabled for symbol"）。
package dashboard

import (
	"context"
	"fmt"
	"os"
	"testing"

	"nox/internal/data/adapter"
	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
	"nox/internal/store/pgstore"
)

func TestMTTradabilityProbe(t *testing.T) {
	if os.Getenv("NOX_DEMO_ORDER") != "1" {
		t.Skip("NOX_DEMO_ORDER=1 才跑（手动 gate，只读 SymbolParams 探测）")
	}
	dsn := os.Getenv("NOX_DEMO_PG_DSN")
	if dsn == "" {
		t.Skip("NOX_DEMO_PG_DSN 未设置")
	}

	ctx := context.Background()
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer st.Close()

	accounts, err := st.ListBrokerAccounts(ctx)
	if err != nil {
		t.Fatalf("list broker accounts: %v", err)
	}
	// venue → BTC 符号（canonical BTCUSDT）
	btcByVenue := map[string]string{}
	for i := range accounts {
		a := &accounts[i]
		syms, err := st.ListSymbolMappings(ctx, a.Venue)
		if err != nil {
			t.Fatalf("list symbol mappings %s: %v", a.Venue, err)
		}
		for _, m := range syms {
			if m.Canonical == "BTCUSDT" {
				btcByVenue[a.Venue] = m.BrokerSymbol
			}
		}
	}

	tradeModeName := map[mt5pb.TradeMode]string{
		0: "DISABLED", 1: "LONGONLY", 2: "SHORTONLY", 3: "CLOSEONLY", 4: "FULL",
	}

	for i := range accounts {
		a := &accounts[i]
		sym, ok := btcByVenue[a.Venue]
		if !ok {
			t.Logf("%s: 无 BTCUSDT 映射，跳过", a.Venue)
			continue
		}
		ad := adapter.NewMT5Adapter(a.Venue, a.Host, a.Server, int32(a.Port), a.User, a.Password)
		if err := ad.Connect(ctx); err != nil {
			t.Logf("%s: Connect 失败: %v", a.Venue, err)
			continue
		}
		resp, err := ad.MT5Client().SymbolParams(tokenCtx(ctx, ad.Token()), &mt5pb.SymbolParamsRequest{Id: ad.Token(), Symbol: sym})
		if err != nil {
			t.Logf("%s %s: SymbolParams err: %v", a.Venue, sym, err)
			ad.Stop()
			continue
		}
		if e := resp.GetError(); e != nil {
			t.Logf("%s %s: SymbolParams error: %s", a.Venue, sym, e.GetMessage())
			ad.Stop()
			continue
		}
		sp := resp.GetResult()
		mode := sp.GetSymbolGroup().GetTradeMode()
		name := tradeModeName[mode]
		if name == "" {
			name = fmt.Sprintf("UNKNOWN(%d)", mode)
		}
		cs := sp.GetSymbolInfo().GetContractSize()
		minLots := sp.GetSymbolGroup().GetMinLots()
		t.Logf("%s %s: TradeMode=%d(%s) ContractSize=%.4f MinLots=%.2f", a.Venue, sym, mode, name, cs, minLots)
		ad.Stop()
	}
}
