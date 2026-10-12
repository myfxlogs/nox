package scan

// T1-T4：宇宙扫描器——join/排序/结构选优/告警阈值。
import (
	"context"
	"testing"
	"time"

	"nox/internal/data/fact"
	"nox/internal/store/pgstore"
)

// fakeScanStore 受控 store（facts 行+映射表+落库计数+universe 雷达表）。
type fakeScanStore struct {
	facts       []fact.Fact
	windowMeans map[string]float64        // D-412 F3：kind|venue|symbol → 7d 均值（未设回退点值）
	equiv       []pgstore.AssetEquivEntry // D-043：等价类映射（nil=无覆盖兜底）
	mappings    []pgstore.SymbolMapping
	replaced    []pgstore.CarryUniverseRow
	universe    map[string]pgstore.CexUniverseRow
	statuses    map[string]string // D-388：venue→status（nil=fail-open 全放行）
	aliases     map[string]string // D-464：alias→canonical
	swapFree    map[string]bool   // D-043：swap_free 账户覆写（nil=全非）
}

func (f *fakeScanStore) LatestFactsByKind(ctx context.Context, kind string) ([]fact.Fact, error) {
	// 对齐真实实现 DISTINCT ON (kind,venue,symbol) ts DESC——latest-per-key
	// （D-034：funding 历史样本与最新瞬时同键共存时，瞬时列须取最新行）。
	latest := map[string]fact.Fact{}
	order := []string{}
	for _, x := range f.facts {
		if x.Kind != kind {
			continue
		}
		k := x.Venue + "|" + x.Symbol
		if cur, ok := latest[k]; !ok || x.Ts.After(cur.Ts) {
			if !ok {
				order = append(order, k)
			}
			latest[k] = x
		}
	}
	var out []fact.Fact
	for _, k := range order {
		out = append(out, latest[k])
	}
	return out, nil
}

func (f *fakeScanStore) LatestFacts(_ context.Context, kind, _, _ string) ([]fact.Fact, error) {
	var out []fact.Fact
	for _, f := range f.facts {
		if f.Kind == kind {
			out = append(out, f)
		}
	}
	return out, nil
}

// FundingWindowAvg D-035：窗内聚合（真实谓词 kind='funding'∧venue∧symbol∧ts>since）。
func (f *fakeScanStore) FundingWindowAvg(_ context.Context, venue, symbol string, since time.Time) (float64, time.Time, int, error) {
	var sum float64
	var maxTs time.Time
	var n int
	for _, x := range f.facts {
		if x.Kind == fact.KindFunding && x.Venue == venue && x.Symbol == symbol && x.Ts.After(since) {
			sum += x.Value
			n++
			if x.Ts.After(maxTs) {
				maxTs = x.Ts
			}
		}
	}
	if n == 0 {
		return 0, time.Time{}, 0, nil
	}
	return sum / float64(n), maxTs, n, nil
}

// FactsWindowStats D-412 F3：窗口均值——windowMeans 显式覆盖优先；缺省回退点值
// （真实 DB 中窗口内含最新行，均值≈点值的 fake 等价）；窗口内零样本=ErrNoFactsInWindow。
func (f *fakeScanStore) FactsWindowStats(_ context.Context, kind, venue, symbol string, _ time.Duration) (float64, error) {
	if v, ok := f.windowMeans[kind+"|"+venue+"|"+symbol]; ok {
		return v, nil
	}
	for _, x := range f.facts {
		if x.Kind == kind && x.Venue == venue && x.Symbol == symbol {
			return x.Value, nil
		}
	}
	return 0, pgstore.ErrNoFactsInWindow
}
func (f *fakeScanStore) ListSymbolMappings(_ context.Context, venue string) ([]pgstore.SymbolMapping, error) {
	var out []pgstore.SymbolMapping
	for _, m := range f.mappings {
		if m.Venue == venue {
			out = append(out, m)
		}
	}
	return out, nil
}
func (f *fakeScanStore) ReplaceCarryUniverse(_ context.Context, rows []pgstore.CarryUniverseRow) error {
	f.replaced = rows
	return nil
}

func (f *fakeScanStore) ListSymbolAliases(_ context.Context) (map[string]string, error) {
	if f.aliases == nil {
		return map[string]string{}, nil
	}
	return f.aliases, nil
}

func (f *fakeScanStore) ListCexUniverse(_ context.Context) (map[string]pgstore.CexUniverseRow, error) {
	return f.universe, nil
}

func (f *fakeScanStore) ListAssetEquiv(_ context.Context) ([]pgstore.AssetEquivEntry, error) {
	return f.equiv, nil
}
func (f *fakeScanStore) ListSwapFreeAccounts(_ context.Context) (map[string]bool, error) {
	if f.swapFree == nil {
		return map[string]bool{}, nil // D-028 ④：默认全非 swap_free
	}
	return f.swapFree, nil
}

func (f *fakeScanStore) ListBrokerAccountStatuses(_ context.Context) (map[string]string, error) {
	return f.statuses, nil
}

// TestD336_ScanJoinAndRank T1/T2：BTC（swap 0/0 swap_free）+负 carry canonical——
// BTC 行 net≈6.65 swap_free=true 居首；负行殿后。
func TestD336_ScanJoinAndRank(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "BTCUSDT", Value: 6.65, Ts: time.Now()},
			{Kind: "swap_long", Venue: "277842155", Symbol: "BTCUSD", Value: 0, Ts: time.Now()},
			{Kind: "swap_short", Venue: "277842155", Symbol: "BTCUSD", Value: 0, Ts: time.Now()},
			{Kind: "funding", Venue: "binance", Symbol: "ETHUSDT", Value: 2.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "277842155", Symbol: "ETHUSD", Value: -30, Ts: time.Now()}, // net=2−(−30)=32？语义：多 CFD 成本 swap_long（负=付）→ net=funding−swap_long=2−(−30)=32 正
			{Kind: "funding", Venue: "okx", Symbol: "WTIUSDT", Value: 1.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "277842155", Symbol: "OILUSD", Value: -5, Ts: time.Now()}, // net=1−(−5)=6
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "277842155", BrokerSymbol: "BTCUSD", Canonical: "BTC"},
			{Venue: "277842155", BrokerSymbol: "ETHUSD", Canonical: "ETH"},
			{Venue: "277842155", BrokerSymbol: "OILUSD", Canonical: "WTI"},
		},
	}
	sc := NewCarryUniverseScanner(st)
	res, err := sc.Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// ③：long/short 各发一行——BTC 对=4 行（BTC long/short + ETH long + WTI long）
	if len(res.Rows) != 4 {
		t.Fatalf("rows=%d, want 4（D-028 ③ 双结构行）: %+v", len(res.Rows), res.Rows)
	}
	// 排序（D-412 F1 年化口径）：BTC long 6.65 > ETH 0（suspect 置零）> BTC short −6.65 > WTI −17.25
	if res.Rows[0].Canonical != "BTC" || res.Rows[1].Canonical != "ETH" || res.Rows[2].Canonical != "BTC" || res.Rows[3].Canonical != "WTI" {
		t.Errorf("排序错: %v", res.Rows)
	}
	// T1：BTC swap_free（swap 0/0）net≈funding
	if !res.Rows[0].SwapFree {
		t.Error("BTC swap_long=0 应标记 swap_free")
	}
	if res.Rows[0].NetAnnual < 6.6 || res.Rows[0].NetAnnual > 6.7 {
		t.Errorf("BTC net want ≈6.65, got %v", res.Rows[0].NetAnnual)
	}
	// ③：BTC short 行=−6.65（swap_free 双零 → −funding）
	if res.Rows[2].Structure != "short_cfd_long_perp" || res.Rows[2].NetAnnual > -6.6 || res.Rows[2].NetAnnual < -6.7 {
		t.Errorf("BTC short 行 want −6.65, got %+v", res.Rows[2])
	}
	// F1：ETH swap −30bps_daily=−109.5%/yr → suspect 置零（年化本义恢复）
	if res.Rows[1].Note == "" || res.Rows[1].NetAnnual != 0 {
		t.Errorf("ETH swap 年化 109.5%%>100 应 suspect 置零: %+v", res.Rows[1])
	}
	// 落库
	if err := st.ReplaceCarryUniverse(context.Background(), toPgRows(res.Rows)); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(st.replaced) != 4 {
		t.Errorf("落库行数=%d, want 4（D-028 ③）", len(st.replaced))
	}
}

// TestD336_StructurePick T4（D-412 F2 语义更新）：同 canonical 双 venue swap（0 vs −8.22）
// → 全叉积两行并存不再坍缩取最优；在跑线 277842155 swap_free 恒在监控视野（D-410 保底前提）。
func TestD336_StructurePick(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "XAUUSDT", Value: 5.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "591959217", Symbol: "XAUUSD", Value: -8.22, Ts: time.Now()},
			{Kind: "swap_long", Venue: "277842155", Symbol: "XAUUSD", Value: 0, Ts: time.Now()},
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "591959217", BrokerSymbol: "XAUUSD", Canonical: "XAU"},
			{Venue: "277842155", BrokerSymbol: "XAUUSD", Canonical: "XAU"},
		},
	}
	sc := NewCarryUniverseScanner(st)
	res, err := sc.Run(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("rows=%d skipped=%d, want 2（F2 全叉积不坍缩；swap_short 零候选=仅 long 行，D-028 ③ 双行只发可构建结构）: %+v", len(res.Rows), res.Skipped, res.Rows)
	}
	byVenue := map[string]UniverseRow{}
	for _, r := range res.Rows {
		if r.CexVenue != "binance" {
			t.Errorf("cex venue 应忠实保留行源（binance），got %q", r.CexVenue)
		}
		byVenue[r.MtVenue] = r
	}
	r := byVenue["277842155"]
	// 在跑线：swap=0 → net=5（swap_free），不因另一 venue 成本优而消失（D-410 保底锚）
	if r.MtVenue == "" || !r.SwapFree || r.NetAnnual < 4.99 || r.NetAnnual > 5.01 {
		t.Errorf("277842155 swap_free 行应恒在 net=5, got %+v", r)
	}
	// 次优 venue 行保留（年化口径：−8.22bps_daily=−30.00%/yr → net=5−30=−25）
	if o := byVenue["591959217"]; o.MtVenue == "" || o.SwapFree || o.NetAnnual < -25.01 || o.NetAnnual > -24.99 {
		t.Errorf("591959217 行应保留 net=−25（年化），got %+v", o)
	}
}

// TestD336_CanonicalJoin T3：CEX 无 USDT 后缀/MT 无映射 → skip 计数。
func TestD336_CanonicalJoin(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "BTCUSD", Value: 5.0, Ts: time.Now()}, // 无 USDT 后缀
			{Kind: "swap_long", Venue: "ghost", Symbol: "XXXUSD", Value: -5, Ts: time.Now()},  // 无映射
		},
		mappings: []pgstore.SymbolMapping{},
	}
	sc := NewCarryUniverseScanner(st)
	res, err := sc.Run(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Rows) != 0 || res.Skipped != 2 {
		t.Errorf("无法映射应全 skip: rows=%d skipped=%d, want 0/2", len(res.Rows), res.Skipped)
	}
}

func containsSub(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func toPgRows(rows []UniverseRow) []pgstore.CarryUniverseRow {
	out := make([]pgstore.CarryUniverseRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, pgstore.CarryUniverseRow{
			Canonical: r.Canonical, MtVenue: r.MtVenue, CexVenue: r.CexVenue,
			NetAnnualPct: r.NetAnnual, FundingAnnualPct: r.FundingAnn,
			SwapLongPct: r.SwapLongPct, SwapShortPct: r.SwapShortPct,
			SwapFree: r.SwapFree, Structure: r.Structure, Note: r.Note,
		})
	}
	return out
}

// TestD345_SuspectGate D-345：任一输入腿 |年化|>100% → 行仍落表但 net=0+swap_free=false+note
// （不删行保观测，剔除排名）。红=现净 carry 481.5 窜首霸榜（DOT 脏源实证形态：
// broker 1314615 DOTUSDT swap_long=−469.5%/yr 离谱定价）。
func TestD345_SuspectGate(t *testing.T) {
	st := &fakeScanStore{
		facts: []fact.Fact{
			{Kind: "funding", Venue: "binance", Symbol: "BTCUSDT", Value: 6.65, Ts: time.Now()},
			{Kind: "swap_long", Venue: "277842155", Symbol: "BTCUSD", Value: 0, Ts: time.Now()},
			{Kind: "swap_short", Venue: "277842155", Symbol: "BTCUSD", Value: 0, Ts: time.Now()},
			{Kind: "funding", Venue: "binance", Symbol: "DOTUSDT", Value: 12.0, Ts: time.Now()},
			{Kind: "swap_long", Venue: "1314615", Symbol: "DOTUSDT", Value: -128.63, Ts: time.Now()}, // 脏源（D-412 F1：facts 单位=bps_daily，−128.63×3.65≈−469.5%/yr）
		},
		mappings: []pgstore.SymbolMapping{
			{Venue: "277842155", BrokerSymbol: "BTCUSD", Canonical: "BTC"},
			{Venue: "1314615", BrokerSymbol: "DOTUSDT", Canonical: "DOT"},
		},
	}
	res, err := NewCarryUniverseScanner(st).Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("rows=%d, want 3（D-028 ③ BTC long/short 双行）: %+v", len(res.Rows), res.Rows)
	}
	// 剔除排名：健康 BTC long 居首，suspect DOT 中位，BTC short −6.65 垫底
	if res.Rows[0].Canonical != "BTC" || res.Rows[1].Canonical != "DOT" {
		t.Fatalf("排序错（suspect 行应垫底）: %+v", res.Rows)
	}
	dot := res.Rows[1]
	if dot.NetAnnual != 0 {
		t.Errorf("DOT net = %v, want 0（红=现 481.5 上榜）", dot.NetAnnual)
	}
	if dot.SwapFree {
		t.Error("DOT swap_free = true, want false（脏源非免息）")
	}
	if dot.Note != "suspect: swap 469.5%" {
		t.Errorf("DOT note = %q, want %q", dot.Note, "suspect: swap 469.5%")
	}
	// 健康行不受门误伤：note 空、net 正常、swap_free 保持
	btc := res.Rows[0]
	if btc.Note != "" || btc.NetAnnual < 6.6 || !btc.SwapFree {
		t.Errorf("BTC 健康行被门误伤: %+v", btc)
	}
}

// TestD388_StoppedVenueExcluded S1.3：broker 托管 venue status=stopped → 其 swap facts
// 退出宇宙配对（funding 单侧无对 → 零行）；同 venue connected 时三行全出（对照组）。
func TestD388_StoppedVenueExcluded(t *testing.T) {
	mk := func(statuses map[string]string) *fakeScanStore {
		return &fakeScanStore{
			facts: []fact.Fact{
				{Kind: "funding", Venue: "binance", Symbol: "BTCUSDT", Value: 6.65, Ts: time.Now()},
				{Kind: "swap_long", Venue: "277842155", Symbol: "BTCUSD", Value: 0, Ts: time.Now()},
				{Kind: "swap_short", Venue: "277842155", Symbol: "BTCUSD", Value: 0, Ts: time.Now()},
			},
			mappings: []pgstore.SymbolMapping{
				{Venue: "277842155", BrokerSymbol: "BTCUSD", Canonical: "BTC"},
			},
			statuses: statuses,
		}
	}
	res, err := NewCarryUniverseScanner(mk(map[string]string{"277842155": "stopped"})).Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Rows) != 0 {
		t.Fatalf("stopped venue 应零候选, got %d rows: %+v", len(res.Rows), res.Rows)
	}
	res2, err := NewCarryUniverseScanner(mk(map[string]string{"277842155": "connected"})).Run(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	if len(res2.Rows) == 0 {
		t.Error("connected 对照组应有候选（binance 不在 broker_accounts 放行）")
	}
}
