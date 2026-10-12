// gate_margin_capitalview_test.go —— D-549 R1 三态锚：CEX 资金源 Src 闸——
// ws 过 / rest 过 / snap 拒（执行面禁快照兜底，fail-closed）+ MT venue 快照原语义回归锚。
package gate

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/ledger/reconcile"
	"nox/internal/platform/testutil"
	"nox/internal/portfolio/capitalview"
	"nox/internal/store/pgstore"
)

// fakeMarginFloor 计数 fake：判定本体恒过（Src 闸行为由 gate 层独立断言）。
type fakeMarginFloor struct {
	calls  int
	venues []string
}

func (f *fakeMarginFloor) MarginFloorCheck(ctx context.Context, venueKey string, addNotionalUSD float64) (bool, string) {
	f.calls++
	f.venues = append(f.venues, venueKey)
	return true, "ok"
}

// capViewFor 构造三态 Deps 的统一读口：ledgerSeeded=nil=空账本（snap 态）；
// src="ws"/"rest"=预喂账本条目。snap 态QuerierFor 恒缺（REST 校准败）+快照行在。
func capViewFor(t *testing.T, src string) *capitalview.View {
	t.Helper()
	lg := bus.NewBalanceLedger()
	now := time.Now().UTC()
	if src != "" {
		lg.Put("binance:perp", 100, 110, "USDT", src, now)
	}
	eq := 110.0
	return capitalview.New(capitalview.Deps{
		Ledger:  lg,
		RestTTL: func() time.Duration { return 30 * time.Second },
		QuerierFor: func(ctx context.Context, venueKey string) (reconcile.AccountQuerier, string, bool) {
			// 涟漪：LiveFresh 对 rest TTL 内校准值强制实拉——
			// src=rest 用例须供真 REST 通道（回执与 Put 同值模拟 venue 真实响应）。
			if src == "rest" {
				return &d549r1FakeQuerier{acct: &reconcile.AccountInfo{Equity: 110, FreeMargin: 100, Currency: "USDT"}}, "binance:perp", true
			}
			return nil, "", false // REST 校准通道缺——snap 态走快照兜底
		},
		Snapshots: func(ctx context.Context) ([]pgstore.VenueAccountLatest, error) {
			return []pgstore.VenueAccountLatest{{
				Venue: "binance", VenueType: "perp",
				EquityRaw: &eq, Currency: "USDT",
				FetchedAt: now.Add(-9 * time.Minute), // 陈旧快照（8-10min 节奏病灶面）
			}}, nil
		},
	})
}

// d549r1FakeQuerier 静态账户 querier（LiveFresh 强制实拉用例喂 REST 通道）。
type d549r1FakeQuerier struct{ acct *reconcile.AccountInfo }

func (f *d549r1FakeQuerier) Account(context.Context) (*reconcile.AccountInfo, error) {
	return f.acct, nil
}
func (f *d549r1FakeQuerier) VenueKey() string { return "binance:perp" }

func capTestGate(t *testing.T, v *capitalview.View) (*Gate, *fakeMarginFloor) {
	t.Helper()
	st, err := pgstore.New(context.Background(), testutil.TestDSN(t, "NOX_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	g := NewGate(st, Config{}, NewControlPlane(st))
	fake := &fakeMarginFloor{}
	g.SetMarginFloorChecker(fake)
	if v != nil {
		g.SetCapitalView(v)
	}
	return g, fake
}

// TestD549R1_WS与Rest过 实拉源（ws/rest）→ Src 闸放行，判定本体被调。
func TestD549R1_WSAndRestPass(t *testing.T) {
	for _, src := range []string{"ws", "rest"} {
		g, fake := capTestGate(t, capViewFor(t, src))
		v := g.marginFloorGateCheck(context.Background(), TradeIntent{
			VenueBuy: "binance:spot", VenueSell: "binance:perp", NotionalUSD: 500,
		})
		if !v.Pass {
			t.Fatalf("src=%s 应过闸, got 拒: %s", src, v.Reason)
		}
		if fake.calls != 2 {
			t.Errorf("src=%s 判定本体应双腿各查一次, got %d", src, fake.calls)
		}
	}
}

// TestD549R1_SnapRejected 快照兜底源 → fail-closed 拒开（判定本体零进，告警落库）。
func TestD549R1_SnapRejected(t *testing.T) {
	g, fake := capTestGate(t, capViewFor(t, ""))
	v := g.marginFloorGateCheck(context.Background(), TradeIntent{
		VenueBuy: "binance:spot", VenueSell: "binance:perp", NotionalUSD: 500,
	})
	if v.Pass {
		t.Fatal("snap 源应 fail-closed 拒开")
	}
	if !strings.Contains(v.Reason, "fail-closed") {
		t.Errorf("拒因应标 fail-closed: %q", v.Reason)
	}
	if fake.calls != 0 {
		t.Errorf("snap 源判定本体不得被调, got %d 次", fake.calls)
	}
}

// TestD549R1_MTVenueUnchanged MT venue 快照即其数据面——Src 闸不适用（原语义回归锚：
// 双腿纯 MT 的 cfd×cfd 结构在快照-only 面照常过闸进判定本体）。
func TestD549R1_MTVenueUnchanged(t *testing.T) {
	g, fake := capTestGate(t, capViewFor(t, "")) // 快照-only 面
	v := g.marginFloorGateCheck(context.Background(), TradeIntent{
		VenueBuy: "3001157:mt5", VenueSell: "3001158:mt5", NotionalUSD: 500,
	})
	if !v.Pass {
		t.Fatalf("MT 腿原语义应不变（快照放行）: %s", v.Reason)
	}
	if fake.calls != 2 {
		t.Errorf("双腿 MT 应各进判定本体一次, got %d", fake.calls)
	}
}
