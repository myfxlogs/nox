package dciexec

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

type fakeStore struct {
	cons      []pgstore.CapabilityConstraint
	quotes    []pgstore.CexDCIQuote // ListDCIQuotes 原样回（opt/invest 过滤在 fake 内做）
	orders    map[string]*pgstore.DCIOrder
	gaps      []pgstore.ConditionGap
	posRows   int
	alerts    []string
	positions []pgstore.PersonalPosition
	updates   []struct {
		id   int64
		qty  float64
		note string
	}
	nextID      int64
	maxTenor    []int // 捕获每次 ListDCIQuotes 的 cap
	outstanding float64
}

func f64(v float64) *float64 { return &v }

func newFake() *fakeStore {
	share := 0.34
	tenor := 7
	deliv := time.Now().AddDate(0, 0, 7)
	return &fakeStore{
		cons: []pgstore.CapabilityConstraint{
			{Subject: "custody_share", Qualifier: "max", ValueNum: &share},
			{Subject: "custody_redeemable", Qualifier: "always", ValueNum: f64(90)},
		},
		quotes: []pgstore.CexDCIQuote{
			{Venue: "binance", Underlying: "BTC", InvestCcy: "BTC",
				Strike: f64(120000), OptType: "CALL", TenorDays: &tenor, APY: 0.08,
				ProductRef: "CALLP", DeliveryTs: &deliv},
			{Venue: "binance", Underlying: "BTC", InvestCcy: "USDT",
				Strike: f64(80000), OptType: "PUT", TenorDays: &tenor, APY: 0.09,
				ProductRef: "PUTP", DeliveryTs: &deliv},
		},
		orders: map[string]*pgstore.DCIOrder{},
	}
}

func (f *fakeStore) ListCapabilityConstraints(context.Context) ([]pgstore.CapabilityConstraint, error) {
	return f.cons, nil
}
func (f *fakeStore) ListDCIQuotes(_ context.Context, v, u, opt, inv string, m int) ([]pgstore.CexDCIQuote, error) {
	f.maxTenor = append(f.maxTenor, m)
	var out []pgstore.CexDCIQuote
	for _, q := range f.quotes {
		if q.Venue == v && q.Underlying == u && q.OptType == opt && q.InvestCcy == inv {
			out = append(out, q)
		}
	}
	return out, nil
}
func (f *fakeStore) CreateDCIOrder(_ context.Context, o *pgstore.DCIOrder) (int64, bool, error) {
	if _, dup := f.orders[o.RequestID]; dup {
		return 0, false, nil
	}
	f.nextID++
	o.ID = f.nextID
	f.orders[o.RequestID] = o
	return o.ID, true, nil
}
func (f *fakeStore) FinishDCIOrder(_ context.Context, id int64, status, resp string) error {
	for _, o := range f.orders {
		if o.ID == id {
			o.Status = status
			o.Response = resp
		}
	}
	return nil
}
func (f *fakeStore) InsertPersonalPosition(context.Context, string, string, string, string, *float64, *time.Time) (int64, error) {
	f.posRows++
	return 1, nil
}
func (f *fakeStore) OutstandingDCIQty(context.Context, string, string) (float64, error) {
	return f.outstanding, nil
}
func (f *fakeStore) ListOpenPersonalPositions(context.Context) ([]pgstore.PersonalPosition, error) {
	return f.positions, nil
}
func (f *fakeStore) UpdatePersonalPositionQtyNote(_ context.Context, id int64, qty float64, note string) error {
	f.updates = append(f.updates, struct {
		id   int64
		qty  float64
		note string
	}{id, qty, note})
	return nil
}
func (f *fakeStore) OpenConditionGap(_ context.Context, g *pgstore.ConditionGap) (int64, error) {
	f.gaps = append(f.gaps, *g)
	return int64(len(f.gaps)), nil
}
func (f *fakeStore) RequestConditionGap(context.Context, int64) error { return nil }
func (f *fakeStore) InsertAlert(_ context.Context, _ *int64, level, msg string) error {
	f.alerts = append(f.alerts, level+": "+msg)
	return nil
}

// execR3 R3 形态 executor：PriceFn/BalanceFn 注入、Sapi=测试服。
func execR3(fs *fakeStore, url string, cli *http.Client, bals map[string]float64) *Executor {
	return &Executor{Store: fs, BnKey: "k", BnSec: "s", Sapi: url, Client: cli, Now: time.Now,
		PriceFn: func(context.Context, string) (float64, error) { return 100000, nil },
		BalanceFn: func(_ context.Context, a string) (float64, error) {
			return bals[a], nil
		}}
}

// TestD191_SignQuery：签名串构造对拍——query 字段齐+HMAC 可验。
func TestD191_SignQuery(t *testing.T) {
	e := &Executor{BnKey: "k", BnSec: "s", Sapi: "https://x.test", Now: time.Now}
	at := time.UnixMilli(1700000000000)
	q, url := e.SubscribeBody("PROJ1", 0.018, at)
	if !strings.Contains(q, "projectId=PROJ1") || !strings.Contains(q, "lots=0.018") {
		t.Fatalf("query 字段缺: %s", q)
	}
	mac := hmac.New(sha256.New, []byte("s"))
	mac.Write([]byte(q))
	want := hex.EncodeToString(mac.Sum(nil))
	if !strings.Contains(url, "signature="+want) ||
		!strings.HasPrefix(url, "https://x.test/v1/dci/product/subscribe?") {
		t.Fatalf("签名/端点错（base 含 /sapi 段从 /vN 起拼）: %s", url)
	}
}

// TestD191_ConstraintBound：缺 custody_share→拒单+constraint 缺口落。
func TestD191_ConstraintBound(t *testing.T) {
	fs := newFake()
	fs.cons = fs.cons[1:]
	e := execR3(fs, "http://x", nil, map[string]float64{"BTC": 0.06})
	_, err := e.Go(context.Background())
	if err == nil || !strings.Contains(err.Error(), "custody_share") {
		t.Fatalf("缺 share 约束应拒单: %v", err)
	}
	if len(fs.gaps) != 1 || fs.gaps[0].Kind != "constraint" || len(fs.orders) != 0 {
		t.Fatalf("constraint 缺口面错: %+v", fs.gaps)
	}
}

// TestD191R3_TenorCapFromConstraint：期限上限=custody_redeemable.value_num=90；
// 行在无数值→14、行缺→30（对拍 ListDCIQuotes 收到 cap）。
func TestD191R3_TenorCapFromConstraint(t *testing.T) {
	cases := []struct {
		name string
		cons []pgstore.CapabilityConstraint
		want int
	}{
		{"value90", []pgstore.CapabilityConstraint{
			{Subject: "custody_share", ValueNum: f64(0.34)},
			{Subject: "custody_redeemable", ValueNum: f64(90)}}, 90},
		{"novalue", []pgstore.CapabilityConstraint{
			{Subject: "custody_share", ValueNum: f64(0.34)},
			{Subject: "custody_redeemable"}}, 14},
		{"absent", []pgstore.CapabilityConstraint{
			{Subject: "custody_share", ValueNum: f64(0.34)}}, 30},
	}
	for _, c := range cases {
		fs := newFake()
		fs.cons = c.cons
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"orderId":1}`)
		}))
		e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"BTC": 0.06})
		if _, err := e.Go(context.Background()); err != nil {
			t.Fatal(err)
		}
		srv.Close()
		if len(fs.maxTenor) == 0 || fs.maxTenor[0] != c.want {
			t.Fatalf("%s: cap=%v want %d", c.name, fs.maxTenor, c.want)
		}
	}
}

// TestD191R3_StrikeGate：%OTM 闸——CALL 档 strike=100000<100000×1.10 滤出→无达标档 gap；
// PUT 档 strike=80000≤100000×0.93 达标成交。
func TestD191R3_StrikeGate(t *testing.T) {
	fs := newFake()
	fs.quotes[0].Strike = f64(105000) // CALL 档低于 110000 界→滤出
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"BTC": 0.06, "USDT": 5000})
	msg, err := e.Go(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 { // 仅 PUT 腿 POST
		t.Fatalf("CALL 档应被 OTM 闸滤出: calls=%d", calls)
	}
	foundCallGap := false
	for _, g := range fs.gaps {
		if strings.Contains(g.RequestText, "CALL腿无达标档") {
			foundCallGap = true
		}
	}
	if !foundCallGap || !strings.Contains(msg, "PUT腿申购已提交") {
		t.Fatalf("闸面错: gaps=%+v msg=%s", fs.gaps, msg)
	}
	// 反转：PUT strike 高于 93000 界→两腿皆无档。
	fs2 := newFake()
	fs2.quotes[0].Strike = f64(105000)
	fs2.quotes[1].Strike = f64(95000)
	e2 := execR3(fs2, srv.URL, srv.Client(), map[string]float64{"BTC": 0.06, "USDT": 5000})
	if _, err := e2.Go(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fs2.gaps) != 2 || len(fs2.orders) != 0 {
		t.Fatalf("双腿无档应各落 gap 不降级凑单: gaps=%d orders=%d", len(fs2.gaps), len(fs2.orders))
	}
}

// TestD191R3_QtyBalanceShare：qty=币安实时余额×custody_share（禁读持仓静态行）。
func TestD191R3_QtyBalanceShare(t *testing.T) {
	fs := newFake()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"BTC": 0.2, "USDT": 10000})
	if _, err := e.Go(context.Background()); err != nil {
		t.Fatal(err)
	}
	var callO, putO *pgstore.DCIOrder
	for _, o := range fs.orders {
		if o.OptType == "CALL" {
			callO = o
		} else {
			putO = o
		}
	}
	if callO == nil || math.Abs(callO.Qty-0.2*0.34) > 1e-9 {
		t.Fatalf("CALL qty≠余额×share: %+v", callO)
	}
	if putO == nil || math.Abs(putO.Qty-10000*0.34) > 1e-9 || putO.InvestCcy != "USDT" {
		t.Fatalf("PUT 腿参数错: %+v", putO)
	}
}

// TestD191_Idempotent：同参二次 go→request_id 防重无重发（每腿 POST 恰一次）。
func TestD191_Idempotent(t *testing.T) {
	fs := newFake()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"BTC": 0.2, "USDT": 10000})
	if _, err := e.Go(context.Background()); err != nil {
		t.Fatal(err)
	}
	msg, err := e.Go(context.Background())
	if err != nil || !strings.Contains(msg, "防重") {
		t.Fatalf("二次应防重回执: %v %v", msg, err)
	}
	if calls != 2 { // 双腿各一次
		t.Fatalf("POST 应恰两次（双腿）, got %d", calls)
	}
}

// TestD191_FailToGap：下单失败→order failed+dual_order 权限缺口（不重试）。
func TestD191_FailToGap(t *testing.T) {
	fs := newFake()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(403)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"BTC": 0.2, "USDT": 0})
	// USDT=0→PUT 腿跳过，仅 CALL 腿失败
	if _, err := e.Go(context.Background()); err != nil {
		t.Fatal(err) // Go 双腿汇总不返 err（腿级失败入消息）
	}
	if calls != 1 {
		t.Fatalf("单次尝试不重试, got %d", calls)
	}
	var o *pgstore.DCIOrder
	for _, oo := range fs.orders {
		o = oo
	}
	if o.Status != "failed" {
		t.Fatalf("order 应 failed: %s", o.Status)
	}
	found := false
	for _, g := range fs.gaps {
		if g.Subject == "dual_order" {
			found = true
		}
	}
	if !found {
		t.Fatal("权限缺口未落")
	}
}

// TestD191R3_ReconcilePool：漂移>阈→warn 告警+份额行自动修正；挖矿增量注记不入归因。
func TestD191R3_ReconcilePool(t *testing.T) {
	fs := newFake()
	poolQty := 0.05
	fs.positions = []pgstore.PersonalPosition{
		{ID: 7, Asset: "BTC", Kind: "other", Status: "OPEN", Qty: &poolQty},
	}
	// 实有 0.08 BTC×0.34=0.0272 vs 账 0.05→漂移（Δ=-0.0228 远超阈）
	e := execR3(fs, "http://x", nil, map[string]float64{"BTC": 0.08})
	ledger, live, drift, err := e.ReconcilePoolOnce(context.Background())
	if err != nil || !drift {
		t.Fatalf("对账: %v drift=%v", err, drift)
	}
	if math.Abs(ledger-0.05) > 1e-9 || math.Abs(live-0.08*0.34) > 1e-9 {
		t.Fatalf("账/实错: %.6f %.6f", ledger, live)
	}
	if len(fs.updates) != 1 || fs.updates[0].id != 7 ||
		!strings.Contains(fs.updates[0].note, "自动修正") ||
		!strings.Contains(fs.updates[0].note, "池外流入") {
		t.Fatalf("自动修正 note 面错: %+v", fs.updates)
	}
	if len(fs.alerts) != 1 || !strings.HasPrefix(fs.alerts[0], "warn") {
		t.Fatalf("warn 告警缺: %+v", fs.alerts)
	}
	// 无漂移：余额=0.05/0.34→live=0.05→零告警零修正。
	fs2 := newFake()
	fs2.positions = fs.positions
	e2 := execR3(fs2, "http://x", nil, map[string]float64{"BTC": 0.05 / 0.34})
	if _, _, d, err := e2.ReconcilePoolOnce(context.Background()); err != nil || d {
		t.Fatalf("无漂移应静默: %v %v", err, d)
	}
	if len(fs2.updates)+len(fs2.alerts) != 0 {
		t.Fatal("无漂移不应修正/告警")
	}
}

// TestD191R4_URLJoin：base 含 /api|/sapi 段尾，路径从 /vN 起拼——
// 三端点 URL 无双段 404（R4 退回注对拍锚）。
func TestD191R4_URLJoin(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case strings.Contains(r.URL.Path, "ticker/price"):
			fmt.Fprint(w, `{"symbol":"BTCUSDT","price":"100000.0"}`)
		case strings.Contains(r.URL.Path, "/account"):
			fmt.Fprint(w, `{"balances":[{"asset":"BTC","free":"0.1","locked":"0.01"}]}`)
		default:
			fmt.Fprint(w, `{"orderId":1}`)
		}
	}))
	defer srv.Close()
	e := &Executor{Store: newFake(), BnKey: "k", BnSec: "s",
		Sapi: srv.URL + "/sapi", APIBase: srv.URL + "/api",
		Client: srv.Client(), Now: time.Now}
	if _, err := e.spotPrice(context.Background(), "BTCUSDT"); err != nil {
		t.Fatal(err)
	}
	bal, err := e.freeBalance(context.Background(), "BTC")
	if err != nil || bal != 0.11 { // free+locked=0.1+0.01
		t.Fatalf("freeBalance: %v %v", bal, err)
	}
	if _, err := e.post(context.Background(), "P1", 0.01, time.Now()); err != nil {
		t.Fatal(err)
	}
	want := []string{"/api/v3/ticker/price", "/api/v3/account", "/sapi/v1/dci/product/subscribe"}
	if len(paths) != 3 {
		t.Fatalf("paths=%v", paths)
	}
	for i, w := range want {
		if paths[i] != w {
			t.Fatalf("URL 段错 #%d: got %s want %s", i, paths[i], w)
		}
	}
}

// TestD191R4_OutstandingBackfill：未到期 filled 申购 qty 回填池实有——
// 账 0.05 vs 现货 0.04×share+在外 0.06×share=实 0.034→仍漂移但 live 含在外口径。
func TestD191R4_OutstandingBackfill(t *testing.T) {
	fs := newFake()
	poolQty := 0.034
	fs.positions = []pgstore.PersonalPosition{
		{ID: 7, Asset: "BTC", Kind: "other", Status: "OPEN", Qty: &poolQty},
	}
	fs.outstanding = 0.06 // 在外申购 qty
	// 余额 0.04×0.34 + 在外 0.06×0.34 = 0.034 = 账 → 零漂移。
	e := execR3(fs, "http://x", nil, map[string]float64{"BTC": 0.04})
	ledger, live, drift, err := e.ReconcilePoolOnce(context.Background())
	if err != nil || drift {
		t.Fatalf("回填后应零漂移: %v drift=%v", err, drift)
	}
	if math.Abs(live-(0.04+0.06)*0.34) > 1e-9 || math.Abs(ledger-0.034) > 1e-9 {
		t.Fatalf("账/实错: %.6f %.6f", ledger, live)
	}
	if len(fs.updates)+len(fs.alerts) != 0 {
		t.Fatal("零漂移不应修正/告警")
	}
}

// TestD191R5_LargeAccountJSON：/v3/account 大 JSON（>8KB）读径不截断（R5 锚）。
func TestD191R5_LargeAccountJSON(t *testing.T) {
	// 构造 >8KB balances 响应：大量噪声资产+末位 BTC。
	var sb strings.Builder
	sb.WriteString(`{"balances":[`)
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&sb, `{"asset":"DUST%03d","free":"0","locked":"0"},`, i)
	}
	sb.WriteString(`{"asset":"BTC","free":"0.5","locked":"0"}]}`)
	if sb.Len() < 8192 {
		t.Fatalf("锚料应 >8KB: %d", sb.Len())
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, sb.String())
	}))
	defer srv.Close()
	e := &Executor{Store: newFake(), BnKey: "k", BnSec: "s",
		APIBase: srv.URL + "/api", Client: srv.Client(), Now: time.Now}
	bal, err := e.freeBalance(context.Background(), "BTC")
	if err != nil || bal != 0.5 {
		t.Fatalf("大 JSON 应解析出末位 BTC=0.5: %v %v", bal, err)
	}
}
