// swap_diag_probe_test.go —— WP0 诊断探针（D-060 设计前置事实采集，跑完即弃或并入 G2 工具）。
// 对全部 MT5 venue × 已映射符号 dump SymbolParams 原始值：TradeMode / SwapType / SwapLong /
// SwapShort / Point / ContractSize，并按 mt_swap.go 现行归类标注采集器会怎么处理（emit/emit0/skip）。
// 仅当 ARB_SWAP_PROBE=1 且 ARB_DEMO_PG_DSN 设置时运行，只读不下单。
package dashboard

import (
	"context"
	"os"
	"testing"

	"arb/internal/data/adapter"
	mt5pb "arb/internal/data/adapter/gen/mtapi/mt5"
	"arb/internal/store/pgstore"
)

func TestSwapDiagProbe(t *testing.T) {
	if os.Getenv("ARB_SWAP_PROBE") != "1" {
		t.Skip("ARB_SWAP_PROBE=1 才跑（只读 SymbolParams 诊断）")
	}
	dsn := os.Getenv("ARB_DEMO_PG_DSN")
	if dsn == "" {
		t.Skip("ARB_DEMO_PG_DSN 未设置")
	}

	ctx := context.Background()
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	defer st.Close()

	accounts, err := st.ListBrokerAccounts(ctx)
	if err != nil {
		t.Fatalf("list broker accounts: %v", err)
	}

	for i := range accounts {
		a := &accounts[i]
		maps, err := st.ListSymbolMappings(ctx, a.Venue)
		if err != nil {
			t.Fatalf("list symbol mappings %s: %v", a.Venue, err)
		}
		ad := adapter.NewMT5Adapter(a.Venue, a.Host, a.Server, int32(a.Port), a.User, a.Password)
		if err := ad.Connect(ctx); err != nil {
			t.Logf("=== %s (%s): Connect 失败: %v", a.Venue, a.Server, err)
			continue
		}
		t.Logf("=== %s (%s)", a.Venue, a.Server)
		for _, m := range maps {
			resp, err := ad.MT5Client().SymbolParams(tokenCtx(ctx, ad.Token()), &mt5pb.SymbolParamsRequest{Id: ad.Token(), Symbol: m.BrokerSymbol})
			if err != nil {
				t.Logf("  %s(%s): RPC err: %v", m.BrokerSymbol, m.Canonical, err)
				continue
			}
			if e := resp.GetError(); e != nil {
				t.Logf("  %s(%s): error resp: %s", m.BrokerSymbol, m.Canonical, e.GetMessage())
				continue
			}
			r := resp.GetResult()
			if r == nil {
				t.Logf("  %s(%s): result=nil", m.BrokerSymbol, m.Canonical)
				continue
			}
			sg, si := r.GetSymbolGroup(), r.GetSymbolInfo()
			if sg == nil {
				t.Logf("  %s(%s): symbolGroup=nil", m.BrokerSymbol, m.Canonical)
				continue
			}
			st_ := sg.GetSwapType()
			l, s := sg.GetSwapLong(), sg.GetSwapShort()
			verdict := "emit"
			switch {
			case st_ == mt5pb.SwapType_SwapType_SwapNone && (l != 0 || s != 0):
				verdict = "SKIP(矛盾: None+非零)"
			case st_ == mt5pb.SwapType_SwapType_SwapNone:
				verdict = "emit0(None)"
			case st_ == mt5pb.SwapType_SwapType_InPoints,
				st_ == mt5pb.SwapType_SwapType_MarginCurrency,
				st_ == mt5pb.SwapType_SwapType_Currency,
				st_ == mt5pb.SwapType_SwapType_PercCurPrice,
				st_ == mt5pb.SwapType_SwapType_PercOpenPrice:
			default:
				verdict = "SKIP(未标定)"
			}
			point, cs := 0.0, 0.0
			if si != nil {
				point, cs = si.GetPoints(), si.GetContractSize()
			}
			t.Logf("  %s(%s): TradeMode=%d SwapType=%d(%s) L=%.6f S=%.6f Point=%.8f CS=%.2f MinLot=%.2f → %s",
				m.BrokerSymbol, m.Canonical, sg.GetTradeMode(), int32(st_), st_.String(), l, s, point, cs, sg.GetMinLots(), verdict)
		}
		ad.Stop()
	}
}
