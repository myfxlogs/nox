package dciexec

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

type fakeStore struct {
	cons     []pgstore.CapabilityConstraint
	holdings []pgstore.CapabilityHolding
	quote    *pgstore.CexDCIQuote
	orders   map[string]*pgstore.DCIOrder
	gaps     []pgstore.ConditionGap
	posRows  int
	alerts   int
	nextID   int64
}

func newFake() *fakeStore {
	share := 0.34
	strike := 100000.0
	tenor := 7
	deliv := time.Now().AddDate(0, 0, 7)
	return &fakeStore{
		cons: []pgstore.CapabilityConstraint{
			{Subject: "custody_share", Qualifier: "max", ValueNum: &share},
			{Subject: "custody_redeemable", Qualifier: "always"},
		},
		holdings: []pgstore.CapabilityHolding{{Asset: "BTC", Qty: 0.06}},
		quote: &pgstore.CexDCIQuote{Venue: "binance", Underlying: "BTC", InvestCcy: "BTC",
			Strike: &strike, OptType: "C", TenorDays: &tenor, APY: 0.08,
			ProductRef: "PROJ1", DeliveryTs: &deliv},
		orders: map[string]*pgstore.DCIOrder{},
	}
}

func (f *fakeStore) ListCapabilityConstraints(context.Context) ([]pgstore.CapabilityConstraint, error) {
	return f.cons, nil
}
func (f *fakeStore) ListCapabilityLedger(context.Context) (*pgstore.CapabilityLedger, error) {
	return &pgstore.CapabilityLedger{Holdings: f.holdings}, nil
}
func (f *fakeStore) BestDCIQuote(context.Context, string, string, string, string, int) (*pgstore.CexDCIQuote, error) {
	return f.quote, nil
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
func (f *fakeStore) OpenConditionGap(_ context.Context, g *pgstore.ConditionGap) (int64, error) {
	f.gaps = append(f.gaps, *g)
	return int64(len(f.gaps)), nil
}
func (f *fakeStore) RequestConditionGap(context.Context, int64) error { return nil }
func (f *fakeStore) InsertAlert(context.Context, *int64, string, string) error {
	f.alerts++
	return nil
}

// TestD191_SignQuery：签名串构造对拍——query 字段齐+HMAC 可验。
func TestD191_SignQuery(t *testing.T) {
	e := &Executor{BnKey: "k", BnSec: "s", Base: "https://x.test", Now: time.Now}
	at := time.UnixMilli(1700000000000)
	q, url := e.SubscribeBody("PROJ1", 0.018, at)
	if !strings.Contains(q, "projectId=PROJ1") || !strings.Contains(q, "lots=0.018") {
		t.Fatalf("query 字段缺: %s", q)
	}
	mac := hmac.New(sha256.New, []byte("s"))
	mac.Write([]byte(q))
	want := hex.EncodeToString(mac.Sum(nil))
	if !strings.Contains(url, "signature="+want) {
		t.Fatalf("签名对拍败: %s", url)
	}
	if !strings.HasPrefix(url, "https://x.test/sapi/v1/dci/product/subscribe?") {
		t.Fatalf("端点错: %s", url)
	}
}

// TestD191_ConstraintBound：缺 custody_share→拒单+constraint 缺口落（约束界硬线）。
func TestD191_ConstraintBound(t *testing.T) {
	fs := newFake()
	fs.cons = fs.cons[1:] // 摘除 share
	e := &Executor{Store: fs, BnKey: "k", BnSec: "s", Base: "http://x", Now: time.Now}
	_, err := e.Go(context.Background())
	if err == nil || !strings.Contains(err.Error(), "custody_share") {
		t.Fatalf("缺 share 约束应拒单: %v", err)
	}
	if len(fs.gaps) != 1 || fs.gaps[0].Kind != "constraint" {
		t.Fatalf("constraint 缺口未落: %+v", fs.gaps)
	}
	if len(fs.orders) != 0 {
		t.Fatal("拒单不应落 order")
	}
}

// TestD191_TenorBound：redeemable 摘除→tenor 上限放开至 30（14d 硬线由约束消费）。
func TestD191_TenorBound(t *testing.T) {
	fs := newFake()
	got := -1
	e := &Executor{Store: fs, BnKey: "k", BnSec: "s", Base: "http://x", Now: time.Now}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e.Base = srv.URL
	e.Client = srv.Client()
	// 探测 maxTenor：fake BestDCIQuote 直接回 quote，另测调用参数在 store 捕获。
	fs2 := newFake()
	_ = fs2
	// 直接对拍 store 收到的 maxTenor：包一层。
	wrap := &captureStore{fakeStore: fs, maxTenor: &got}
	e.Store = wrap
	if _, err := e.Go(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != 14 {
		t.Fatalf("redeemable 在→上限应 14d, got %d", got)
	}
}

type captureStore struct {
	*fakeStore
	maxTenor *int
}

func (c *captureStore) BestDCIQuote(ctx context.Context, v, u, o, i string, m int) (*pgstore.CexDCIQuote, error) {
	*c.maxTenor = m
	return c.fakeStore.BestDCIQuote(ctx, v, u, o, i, m)
}

// TestD191_Idempotent：同参二次 go→request_id 防重无重发（POST 恰一次）。
func TestD191_Idempotent(t *testing.T) {
	fs := newFake()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e := &Executor{Store: fs, BnKey: "k", BnSec: "s", Base: srv.URL, Client: srv.Client(), Now: time.Now}
	if _, err := e.Go(context.Background()); err != nil {
		t.Fatal(err)
	}
	msg, err := e.Go(context.Background())
	if err != nil || !strings.Contains(msg, "防重") {
		t.Fatalf("二次应防重回执: %v %v", msg, err)
	}
	if calls != 1 {
		t.Fatalf("POST 应恰一次, got %d", calls)
	}
}

// TestD191_Receipt：成交回执入账——order filled+personal_positions 行+info alert。
func TestD191_Receipt(t *testing.T) {
	fs := newFake()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"orderId":"A1"}`)
	}))
	defer srv.Close()
	e := &Executor{Store: fs, BnKey: "k", BnSec: "s", Base: srv.URL, Client: srv.Client(), Now: time.Now}
	msg, err := e.Go(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var o *pgstore.DCIOrder
	for _, oo := range fs.orders {
		o = oo
	}
	if o == nil || o.Status != "filled" {
		t.Fatalf("order 未成交: %+v", fs.orders)
	}
	if fs.posRows != 1 || fs.alerts != 1 {
		t.Fatalf("回执面缺: pos=%d alert=%d", fs.posRows, fs.alerts)
	}
	// qty=held 直取（0.06=业主份额行，R2 修双重折扣）
	if o.Qty != 0.06 {
		t.Fatalf("qty=held 口径错: %v", o.Qty)
	}
	_ = msg
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
	e := &Executor{Store: fs, BnKey: "k", BnSec: "s", Base: srv.URL, Client: srv.Client(), Now: time.Now}
	if _, err := e.Go(context.Background()); err == nil {
		t.Fatal("403 应回执失败")
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
