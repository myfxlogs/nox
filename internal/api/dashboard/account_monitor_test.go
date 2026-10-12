package dashboard

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/ledger/reconcile"
	"arb/internal/store/pgstore"
	"arb/internal/trading/exec"

	v1 "arb/internal/gen/dashboardv1"
)

// fakeAccountQuerier 受控账户查询器（D-053 monitor 测试）：acct/err 可随时切换。
type fakeAccountQuerier struct {
	key  string
	acct *reconcile.AccountInfo
	err  error
}

func (f *fakeAccountQuerier) VenueKey() string { return f.key }
func (f *fakeAccountQuerier) Account(context.Context) (*reconcile.AccountInfo, error) {
	return f.acct, f.err
}

// newAcctMonTest 组装 AccountMonitor：nil store + 静态 fake 查询器。
func newAcctMonTest(t *testing.T, q *fakeAccountQuerier) (*AccountMonitor, *bus.Topic[*v1.AccountEvent], <-chan *v1.AccountEvent) {
	t.Helper()
	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	topic := bus.NewTopic[*v1.AccountEvent]()
	ch, _ := topic.Subscribe()
	m := NewAccountMonitor(bm, topic)
	m.AddStatic(q)
	return m, topic, ch
}

// TestAccountMonitor_OfflineKeepsSnapshot：在线有值 → 离线保留最近快照并标 online=false → 恢复更新（D-053）。
func TestAccountMonitor_OfflineKeepsSnapshot(t *testing.T) {
	ctx := context.Background()
	q := &fakeAccountQuerier{key: "binance:spot", acct: &reconcile.AccountInfo{
		VenueKey: "binance:spot", Venue: "binance", VenueType: "spot", AssetType: "cex",
		Balance: 1000, Equity: 1000, Currency: "USDT",
	}}
	m, _, _ := newAcctMonTest(t, q)

	got := m.Refresh(ctx)
	if len(got) != 1 {
		t.Fatalf("online snapshot want 1 account, got %d", len(got))
	}
	if !got[0].Online {
		t.Errorf("online want Online=true, got false")
	}
	if got[0].Equity != 1000 {
		t.Errorf("equity want 1000, got %v", got[0].Equity)
	}

	// 离线 → 保留最近快照，online=false。
	q.err = errors.New("connection refused")
	got = m.Refresh(ctx)
	if len(got) != 1 {
		t.Fatalf("offline snapshot want retained 1 account, got %d", len(got))
	}
	if got[0].Online {
		t.Errorf("offline want Online=false, got true")
	}
	if got[0].Equity != 1000 {
		t.Errorf("offline retained equity want 1000, got %v", got[0].Equity)
	}

	// 恢复在线（权益变化）→ 更新。
	q.err = nil
	q.acct.Equity = 1100
	q.acct.Balance = 1100
	got = m.Refresh(ctx)
	if len(got) != 1 || got[0].Equity != 1100 {
		t.Fatalf("recovered want equity 1100, got %+v", got)
	}
	if !got[0].Online {
		t.Errorf("recovered want Online=true")
	}
}

// TestAccountMonitor_BroadcastOnChangeOnly：仅变更广播；权益变化也广播（指纹含值），无变化不重复推。
func TestAccountMonitor_BroadcastOnChangeOnly(t *testing.T) {
	ctx := context.Background()
	q := &fakeAccountQuerier{key: "277842155:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "277842155:mt5", Venue: "277842155", VenueType: "mt5", AssetType: "cfd",
		Balance: 5000, Equity: 5100, FreeMargin: 2000, Currency: "USD",
	}}
	m, _, ch := newAcctMonTest(t, q)

	drain := func() int {
		n := 0
		for {
			select {
			case <-ch:
				n++
			default:
				return n
			}
		}
	}

	if n := drain(); n != 0 {
		t.Fatalf("before any poll want 0 broadcasts, got %d", n)
	}
	m.Refresh(ctx) // 首次：空 → 有值，变更
	if n := drain(); n != 1 {
		t.Fatalf("first poll want 1 broadcast, got %d", n)
	}
	m.Refresh(ctx) // 无变化
	if n := drain(); n != 0 {
		t.Fatalf("no-change poll want 0 broadcasts, got %d", n)
	}

	q.acct.Equity = 5120 // 权益跳变 → 再广播
	m.Refresh(ctx)
	if n := drain(); n != 1 {
		t.Fatalf("equity-change poll want 1 broadcast, got %d", n)
	}
}

// TestResolveExchangeBaseURL_VenueTypeAware：binance base 推导按 venue_type 分流——spot 从
// fapi 风格 base 推导 spot base；perp 保留 demofapi 前缀不动（naive replace fapi→api 会把
// demofapi 错改成 demoapi，D-059 实施修正② 复核实测：模拟盘合约在独立子域 demo-fapi.binance.com）。
func TestResolveExchangeBaseURL_VenueTypeAware(t *testing.T) {
	t.Setenv("BINANCE_PROXY_URL", "") // 空默认 → 无代理直连 fapi
	spot := resolveExchangeBaseURL(pgstore.ExchangeConfig{Venue: "binance", VenueType: "spot", BaseURL: "https://192.229.85.35:8443/demo"})
	if spot != "https://192.229.85.35:8443/demo" {
		t.Errorf("spot demo base want unchanged, got %q", spot)
	}
	perp := resolveExchangeBaseURL(pgstore.ExchangeConfig{Venue: "binance", VenueType: "perp", BaseURL: "https://192.229.85.35:8443/demofapi"})
	if perp != "https://192.229.85.35:8443/demofapi" {
		t.Errorf("perp demofapi base want unchanged (勿 fapi→api 错改), got %q", perp)
	}
	perpEmpty := resolveExchangeBaseURL(pgstore.ExchangeConfig{Venue: "binance", VenueType: "perp"})
	if perpEmpty != "https://fapi.binance.com" {
		t.Errorf("perp empty base want fapi.binance.com default, got %q", perpEmpty)
	}
	spotEmpty := resolveExchangeBaseURL(pgstore.ExchangeConfig{Venue: "binance", VenueType: "spot"})
	if spotEmpty != "https://api.binance.com" {
		t.Errorf("spot empty base want api.binance.com (fapi→api 推导), got %q", spotEmpty)
	}
	okx := resolveExchangeBaseURL(pgstore.ExchangeConfig{Venue: "okx", VenueType: "spot"})
	if okx != "https://www.okx.com" {
		t.Errorf("okx want www.okx.com, got %q", okx)
	}
}

// fakeExchangeStore D-362-R1：撞键锚注入路径（ExchangeStore 接口 fake，双 binance/perp 配置）。
type fakeExchangeStore struct {
	cfgs []pgstore.ExchangeConfig
}

func (f *fakeExchangeStore) ListExchangeConfigs(context.Context) ([]pgstore.ExchangeConfig, error) {
	return f.cfgs, nil
}

// TestAccountMonitor_CollisionKeys D-362-R1 S2 锚：双 binance/perp config（id20 先建
// +id25 后建）→ monitor states 现 binance:perp（后建赢家 canonical）+binance:perp#20
// （输家后缀）两键；AccountInfo 数据字段 Venue/VenueType 保持原值。
// D-516：原 sim 赢家场景改写为双 real 行（simulated=t 行 fail-closed 跳过不入 querier）。
// 旧代码（R0 queriers() 未接线）红=states 仅 1 键后写覆盖。
func TestAccountMonitor_CollisionKeys(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"totalWalletBalance":"345.72","totalMarginBalance":"345.72"}`))
	}))
	defer srv.Close()
	t0 := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	m := NewAccountMonitor(NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute), nil)
	m.SetExchangeStore(&fakeExchangeStore{cfgs: []pgstore.ExchangeConfig{
		{ID: 20, Venue: "binance", VenueType: "perp", APIKey: "k20", APISecret: "s20",
			BaseURL: srv.URL, Simulated: false, CreatedAt: t0},
		{ID: 25, Venue: "binance", VenueType: "perp", APIKey: "k25", APISecret: "s25",
			BaseURL: srv.URL, Simulated: false, CreatedAt: t0.Add(time.Hour)},
		// D-516：sim 行 fail-closed——不建 querier、不入 states、也不抢 canonical 键。
		{ID: 30, Venue: "binance", VenueType: "perp", APIKey: "k30", APISecret: "s30",
			BaseURL: srv.URL, Simulated: true, CreatedAt: t0.Add(2 * time.Hour)},
	}})
	got := m.Refresh(ctx)
	if len(got) != 2 {
		t.Fatalf("snapshot want 2 accounts（双配置现形）, got %d: %+v", len(got), got)
	}
	if len(m.states) != 2 {
		keys := make([]string, 0, len(m.states))
		for k := range m.states {
			keys = append(keys, k)
		}
		t.Fatalf("states want 2 keys, got %d: %v", len(m.states), keys)
	}
	if _, ok := m.states["binance:perp"]; !ok {
		t.Error("states 缺 binance:perp（sim 赢家 canonical）")
	}
	if _, ok := m.states["binance:perp#20"]; !ok {
		t.Error("states 缺 binance:perp#20（real 输家后缀键）")
	}
	for _, a := range got {
		if a.Venue != "binance" || a.VenueType != "perp" {
			t.Errorf("数据字段应保持原值 binance/perp, got %s/%s", a.Venue, a.VenueType)
		}
		if a.Equity != 345.72 {
			t.Errorf("equity = %v, want 345.72", a.Equity)
		}
		if a.Simulated {
			t.Errorf("D-516 起 simulated 行不入 querier——不应出现 Simulated 账户")
		}
		if a.VenueKey != "binance:perp" && a.VenueKey != "binance:perp#20" {
			t.Errorf("VenueKey = %q, want canonical 或 #20 后缀键", a.VenueKey)
		}
	}
}

// TestD132_RestSnapshotWrites D-132 验收锚：REST-only CEX 键（okx:earn 无 WS
// 面）poll 成功+值变 → fin 快照写口落行（src=rest_poll）；同值重 poll → 零写；
// WS 覆盖键（binance:perp）值变也不写（私有流径已有，禁双写）。
func TestD132_RestSnapshotWrites(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var rows []pgstore.VenueAccountSnapshot
	write := func(_ context.Context, snaps []pgstore.VenueAccountSnapshot) error {
		mu.Lock()
		rows = append(rows, snaps...)
		mu.Unlock()
		return nil
	}
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(rows) }

	q := &fakeAccountQuerier{key: "okx:earn", acct: &reconcile.AccountInfo{
		VenueKey: "okx:earn", Venue: "okx", VenueType: "earn", AssetType: "cex",
		Balance: 100, Equity: 120, FreeMargin: 100, Currency: "USDT",
	}}
	m, _, _ := newAcctMonTest(t, q)
	m.SetSnapshotWriter(write)
	m.Refresh(ctx)
	if count() != 1 {
		t.Fatalf("REST-only 键值变应落 1 行, got %d", count())
	}
	mu.Lock()
	r := rows[0]
	mu.Unlock()
	if r.Venue != "okx" || r.VenueType != "earn" || r.EquityRaw != 120 || r.Src != "rest_poll" || r.Currency != "USDT" {
		t.Fatalf("row=%+v", r)
	}
	// 同值重 poll → 零写（acctSame 去重）。
	m.Refresh(ctx)
	if count() != 1 {
		t.Fatalf("值相同不得写, got %d", count())
	}
	// 值变 → 再落一行。
	q.acct = &reconcile.AccountInfo{
		VenueKey: "okx:earn", Venue: "okx", VenueType: "earn", AssetType: "cex",
		Balance: 101, Equity: 121, FreeMargin: 101, Currency: "USDT",
	}
	m.Refresh(ctx)
	if count() != 2 {
		t.Fatalf("值变应再写, got %d", count())
	}
	// D-136：仅非入库字段变（Holdings 浮动子项=earn 每轮 accrual 噪声形态）→ 零写。
	q.acct = &reconcile.AccountInfo{
		VenueKey: "okx:earn", Venue: "okx", VenueType: "earn", AssetType: "cex",
		Balance: 101, Equity: 121, FreeMargin: 101, Currency: "USDT",
		Holdings: []*v1.Holding{{Ccy: "USDT", Qty: 121.00000001, UsdtValue: 121}},
	}
	m.Refresh(ctx)
	if count() != 2 {
		t.Fatalf("非入库字段变不得写, got %d", count())
	}
	// D-136 R1：展示态被账本 patch 污染也不重写——去重基准是 snapBase（上次成功
	// 写行），非可变 st.acct（生产实证：capitalview 校准回写账本→OnLedgerAcct
	// 把 fm 0 patch 成 equity→每轮照写灌行）。
	m.mu.Lock()
	p := *m.states["okx:earn"].acct
	p.FreeMargin = 121
	stP := m.states["okx:earn"]
	stP.acct = &p
	m.states["okx:earn"] = stP
	m.mu.Unlock()
	m.Refresh(ctx)
	if count() != 2 {
		t.Fatalf("展示态污染不得写快照, got %d", count())
	}
	// WS 覆盖键（binance:perp 有私有流）值变也不写。
	q2 := &fakeAccountQuerier{key: "binance:perp", acct: &reconcile.AccountInfo{
		VenueKey: "binance:perp", Venue: "binance", VenueType: "perp", AssetType: "cex",
		Balance: 1, Equity: 2, FreeMargin: 1, Currency: "USDT",
	}}
	m.AddStatic(q2)
	m.Refresh(ctx)
	if count() != 2 {
		t.Fatalf("WS 覆盖键不得双写, got %d", count())
	}
}

// TestD136_LedgerPatchStreamOnly D-136 R1 验收锚：账本 patch 仅限私有流覆盖键——
// REST-only 键（okx:earn）的账本条目是 capitalview REST 校准回写（SrcRest，
// Available=equity 回退），回填会把 fm 0 污染成 equity；binance:perp（有流键）
// 账本 patch 语义保留。
func TestD136_LedgerPatchStreamOnly(t *testing.T) {
	ctx := context.Background()
	q := &fakeAccountQuerier{key: "okx:earn", acct: &reconcile.AccountInfo{
		VenueKey: "okx:earn", Venue: "okx", VenueType: "earn", AssetType: "cex",
		Balance: 169, Equity: 169, FreeMargin: 0, Currency: "USDT",
	}}
	q2 := &fakeAccountQuerier{key: "binance:perp", acct: &reconcile.AccountInfo{
		VenueKey: "binance:perp", Venue: "binance", VenueType: "perp", AssetType: "cex",
		Balance: 10, Equity: 12, FreeMargin: 9, Currency: "USDT",
	}}
	m, _, _ := newAcctMonTest(t, q)
	m.AddStatic(q2)
	l := bus.NewBalanceLedger()
	m.SetBalanceLedger(l)
	m.Refresh(ctx)
	// REST-only 键：账本命中也不 patch（equity-fallback 不回填 FreeMargin）。
	l.Put("okx:earn", 169, 169, "USDT", "rest", time.Now())
	m.OnLedgerAcct("okx:earn")
	m.mu.RLock()
	if got := m.states["okx:earn"].acct.FreeMargin; got != 0 {
		m.mu.RUnlock()
		t.Fatalf("REST-only 键 fm 被账本污染: %v", got)
	}
	m.mu.RUnlock()
	// 私有流覆盖键：账本 patch 照常生效。
	l.Put("binance:perp", 88, 99, "USDT", "ws", time.Now())
	m.OnLedgerAcct("binance:perp")
	m.mu.RLock()
	if got := m.states["binance:perp"].acct.FreeMargin; got != 88 {
		m.mu.RUnlock()
		t.Fatalf("流覆盖键账本 patch 失效: %v", got)
	}
	m.mu.RUnlock()
}

// TestD133_ConfigDeletePrunesGhost D-133 域2 验收锚：删配置行→querier 哈希变→键集淘汰，
// 被删账户行从 Snapshot/states/cur 即刻消失（不再残留到重启），事件流补发一条
// online=false 末态 upsert（既有 proto 无 remove——回放源摘除=新连接绝迹）。
func TestD133_ConfigDeletePrunesGhost(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/account":
			_, _ = w.Write([]byte(`{"balances":[{"asset":"USDT","free":"50","locked":"0"}]}`))
		default: // perp /fapi/v2/account 等
			_, _ = w.Write([]byte(`{"totalWalletBalance":"345.72","totalMarginBalance":"345.72","availableBalance":"300"}`))
		}
	}))
	defer srv.Close()
	t0 := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	store := &fakeExchangeStore{cfgs: []pgstore.ExchangeConfig{
		{ID: 1, Venue: "binance", VenueType: "perp", APIKey: "k", APISecret: "s",
			BaseURL: srv.URL, Simulated: false, CreatedAt: t0},
		{ID: 2, Venue: "binance", VenueType: "spot", APIKey: "k", APISecret: "s",
			BaseURL: srv.URL, Simulated: false, CreatedAt: t0},
	}}
	topic := bus.NewTopic[*v1.AccountEvent]()
	evCh, _ := topic.Subscribe()
	m := NewAccountMonitor(NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute), topic)
	m.SetExchangeStore(store)

	got := m.Refresh(ctx)
	if len(got) != 2 {
		t.Fatalf("初始 snapshot want 2, got %d", len(got))
	}

	// 删 binance:spot 配置行（生产 binance:earn 删除同款场景）→ 哈希变→键集淘汰。
	store.cfgs = store.cfgs[:1]
	got = m.Refresh(ctx)
	if len(got) != 1 || got[0].VenueKey != "binance:perp" {
		t.Fatalf("删行后 snapshot want 仅 binance:perp, got %+v", got)
	}
	if _, ok := m.states["binance:spot"]; ok {
		t.Error("states 残留 binance:spot 幽灵态")
	}
	m.mu.RLock()
	_, inCur := m.cur["binance:spot"]
	m.mu.RUnlock()
	if inCur {
		t.Error("cur 残留 binance:spot 幽灵行（回放源未摘除）")
	}

	// 事件流应收到 binance:spot 的 offline 末态 upsert。
	deadline := time.After(2 * time.Second)
	found := false
	for !found {
		select {
		case ev := <-evCh:
			if ev.Account != nil && ev.Account.VenueKey == "binance:spot" {
				found = true
				if ev.Account.Online {
					t.Error("摘除末事件 want Online=false, got true")
				}
			}
		case <-deadline:
			t.Fatal("未收到 binance:spot 摘除末事件（offline upsert）")
		}
	}
}
