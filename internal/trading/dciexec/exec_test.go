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
	"strconv"
	"strings"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
)

type fakeStore struct {
	cons      []pgstore.CapabilityConstraint
	quotes    []pgstore.CexDCIQuote // ListDCIQuotes 原样回（opt/invest 过滤在 fake 内做）
	orders    map[string]*pgstore.DCIOrder
	listErr   error
	outErr    error
	gaps      []pgstore.ConditionGap
	posRows   int
	alerts    []string
	positions []pgstore.PersonalPosition
	updates   []struct {
		id   int64
		qty  float64
		note string
	}
	nextID         int64
	maxTenor       []int // 捕获每次 ListDCIQuotes 的 cap
	outstanding    float64
	proposals      map[int64]*pgstore.Proposal
	nextProp       int64
	evals          map[int64]*pgstore.OppEval // D-258：提案→评估卡种子（默认 viable）
	strategyStates map[string]string          // D-260：键→lifecycle 态（live 闸种子）
	actualAPRs     map[int64]float64
	// 交割观察面：
	filledOrders []pgstore.FilledDCIOrder
	settled      []struct {
		id          int64
		ccy         string
		qty, income float64
	}
	closedMarkers  []string
	closeN         *int64
	cfgID          int64
	incomeEvs      []pgstore.IncomeEvent
	pits           []pgstore.VenueReject    // D-255：坑表种子/学习捕获
	poolRows       []pgstore.CapitalPoolRow // D-286：earn 敞口闸池快照种子
	paperGateBlock string                   // D-593 接线锚种子：非空=纸面闸拒因
	cfgF           map[string]float64       // D-612：rotation_* 判据键种子
	lastEarnAt     time.Time                // D-612：earn 活动史种子（持有窗）
	hasEarnAt      bool
	jitOutstanding float64               // jit 净额账种子（回转面）
	legs           []pgstore.PositionLeg // jit 回转腿种子（pos 腿 venue 集）
}

// dciListJSON R14 重取产品面测试具——全候选品 canPurchase+orderId 齐帧。
const dciListJSON = `{"list":[
	{"id":"CALLP","orderId":8811,"canPurchase":true},
	{"id":"PUTP","orderId":8822,"canPurchase":true},
	{"id":"NEARP","orderId":8833,"canPurchase":true},
	{"id":"PUTP2","orderId":8844,"canPurchase":true}]}`

// dciListOK product/list+positions GET 分流句柄（handler 首行挂；
// positions 默认空仓——venue 持仓场景在各锚内自覆写）。
func dciListOK(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "product/list") {
		fmt.Fprint(w, dciListJSON)
		return true
	}
	if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "product/positions") {
		fmt.Fprint(w, `{"data":[]}`)
		return true
	}
	return false
}

func f64(v float64) *float64 { return &v }

// testSigma D-526：σ 注入桩（venue klines 不打测）——0.001 小 σ 宽界保 fixtures
// strike 达标（尾损≈0 net>0 过闸）；尾险断言在各专项锚另注 σ。
func testSigma(context.Context) (float64, error) { return 0.001, nil }
func idec(v int) *int                            { return &v }

func newFake() *fakeStore {
	share := 0.34
	tenor := 7
	deliv := time.Now().AddDate(0, 0, 7)
	return &fakeStore{
		cons: []pgstore.CapabilityConstraint{
			{Subject: "custody_share", Qualifier: "max", ValueNum: &share},
			{Subject: "custody_redeemable", Qualifier: "always", ValueNum: f64(90)},
			{Subject: "dci_put_otm_k", Qualifier: "k", ValueNum: f64(1.5)}, // D-526 σ 界 k
			{Subject: "dci_call_otm_k", Qualifier: "k", ValueNum: f64(1.5)},
			{Subject: "dci_tenor_max_days", Qualifier: "max", ValueNum: f64(30)},
			{Subject: "dci_open_notional_cap_usdt", Qualifier: "max", ValueNum: f64(1e9)},
		},
		quotes: []pgstore.CexDCIQuote{
			{Venue: "binance", Underlying: "BTC", InvestCcy: "BTC",
				Strike: f64(120000), OptType: "CALL", TenorDays: &tenor, APY: 0.08,
				ProductRef: "CALLP", OrderRef: "OC1", DeliveryTs: &deliv,
				PurchaseDecimal: idec(4), MinAmount: f64(0.0001), MaxAmount: f64(100)},
			{Venue: "binance", Underlying: "BTC", InvestCcy: "USDT",
				Strike: f64(80000), OptType: "PUT", TenorDays: &tenor, APY: 0.09,
				ProductRef: "PUTP", OrderRef: "OP1", DeliveryTs: &deliv,
				PurchaseDecimal: idec(0), MinAmount: f64(1), MaxAmount: f64(1e6)},
		},
		orders:     map[string]*pgstore.DCIOrder{},
		proposals:  map[int64]*pgstore.Proposal{},
		evals:      map[int64]*pgstore.OppEval{},
		actualAPRs: map[int64]float64{},
	}
}

func (f *fakeStore) ListCapabilityConstraints(context.Context) ([]pgstore.CapabilityConstraint, error) {
	return f.cons, nil
}
func (f *fakeStore) ListDCIQuotes(_ context.Context, v, u, opt, inv string, m int, bound float64) ([]pgstore.CexDCIQuote, error) {
	f.maxTenor = append(f.maxTenor, m)
	var out []pgstore.CexDCIQuote
	for _, q := range f.quotes {
		if q.Venue != v || q.Underlying != u || q.OptType != opt || q.InvestCcy != inv {
			continue
		}
		// R8：fake 同语义界过滤（PUT≤bound/CALL≥bound，bound>0 时 strike 缺=不达标）。
		if bound > 0 {
			if q.Strike == nil {
				continue
			}
			if (opt == "PUT" && *q.Strike > bound) || (opt != "PUT" && *q.Strike < bound) {
				continue
			}
		}
		out = append(out, q)
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
func (f *fakeStore) DCIOrderByID(_ context.Context, id int64) (*pgstore.DCIOrder, error) {
	for _, o := range f.orders {
		if o.ID == id {
			return o, nil
		}
	}
	return nil, nil
}
func (f *fakeStore) UpdatePersonalPositionQtyNote(_ context.Context, id int64, qty float64, note string) error {
	f.updates = append(f.updates, struct {
		id   int64
		qty  float64
		note string
	}{id, qty, note})
	return nil
}

// addProposal 测试播种提案（返 id）。
func (f *fakeStore) addProposal(tmpl string) int64 {
	f.nextProp++
	f.proposals[f.nextProp] = &pgstore.Proposal{
		ID: f.nextProp, TemplateID: tmpl, ExecTrack: "auto",
		Venue: "binance", Status: "proposed"}
	f.evals[f.nextProp] = &pgstore.OppEval{ProposalID: f.nextProp, TemplateID: tmpl, Verdict: "viable"}
	return f.nextProp
}

func (f *fakeStore) ListProposals(_ context.Context, status string, limit int) ([]pgstore.Proposal, error) {
	var out []pgstore.Proposal
	for i := f.nextProp; i >= 1 && len(out) < limit; i-- {
		if p, ok := f.proposals[i]; ok && (status == "" || p.Status == status) {
			out = append(out, *p)
		}
	}
	return out, nil
}
func (f *fakeStore) LatestPendingProposal(_ context.Context, tmpls []string) (*pgstore.Proposal, error) {
	var best *pgstore.Proposal
	for _, p := range f.proposals {
		if p.Status != "proposed" {
			continue
		}
		for _, t := range tmpls {
			if p.TemplateID == t && (best == nil || p.ID > best.ID) {
				best = p
			}
		}
	}
	return best, nil
}
func (f *fakeStore) GetProposal(_ context.Context, id int64) (*pgstore.Proposal, error) {
	p, ok := f.proposals[id]
	if !ok {
		return nil, fmt.Errorf("proposal %d not found", id)
	}
	return p, nil
}
func (f *fakeStore) SetProposalStatus(_ context.Context, id int64, st string) error {
	if p, ok := f.proposals[id]; ok {
		p.Status = st
	}
	return nil
}
func (f *fakeStore) GetOppEval(_ context.Context, id int64) (*pgstore.OppEval, error) {
	return f.evals[id], nil
}
func (f *fakeStore) UpdateOppEvalActual(_ context.Context, id int64, apr float64) error {
	f.actualAPRs[id] = apr
	return nil
}

// strategyStates 测试注入态（零值=live 闸默认关——fail-closed 锚）。
func (f *fakeStore) GetStrategyState(_ context.Context, key string) (string, bool, error) {
	if f.strategyStates == nil {
		return "", false, nil
	}
	st, ok := f.strategyStates[key]
	return st, ok, nil
}

func (f *fakeStore) CapitalPoolSnapshots(context.Context) ([]pgstore.CapitalPoolRow, error) {
	return f.poolRows, nil
}

func (f *fakeStore) OpenConditionGap(_ context.Context, g *pgstore.ConditionGap) (int64, error) {
	f.gaps = append(f.gaps, *g)
	return int64(len(f.gaps)), nil
}
func (f *fakeStore) RequestConditionGap(context.Context, int64) error { return nil }
func (f *fakeStore) DCIOrderLegCount(_ context.Context, propID int64, opt, inv string) (int, error) {
	n := 0
	for _, o := range f.orders {
		if o.ProposalID != nil && *o.ProposalID == propID && o.OptType == opt && o.InvestCcy == inv {
			n++
		}
	}
	return n, nil
}

// ListFailedProposalOrders / OutstandingDCIProduct D-198 锚具。
func (f *fakeStore) ListFailedProposalOrders(context.Context) ([]pgstore.FailedProposalOrder, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []pgstore.FailedProposalOrder
	for _, o := range f.orders {
		if o.ProposalID == nil || o.Status != "failed" {
			continue
		}
		// 最新单判：同 proposal 更大 id 存在则跳过
		newer := false
		for _, o2 := range f.orders {
			if o2.ProposalID != nil && *o2.ProposalID == *o.ProposalID && o2.ID > o.ID {
				newer = true
			}
		}
		if newer {
			continue
		}
		if p := f.proposals[*o.ProposalID]; p != nil && p.Status == "proposed" {
			out = append(out, pgstore.FailedProposalOrder{
				ProposalID: *o.ProposalID, ProductRef: o.ProductRef, OptType: o.OptType})
		}
	}
	return out, nil
}
func (f *fakeStore) OutstandingDCIProduct(_ context.Context, productRef, optType string) (bool, error) {
	if f.outErr != nil {
		return false, f.outErr
	}
	for _, o := range f.orders {
		if o.ProductRef == productRef && o.OptType == optType && o.Status == "filled" {
			return true, nil
		}
	}
	return false, nil
}

// RecentDCIOrderByProposal R11 租约：同 proposal 最新单 id（fake 窗不评=恒内）。
func (f *fakeStore) RecentDCIOrderByProposal(_ context.Context, propID int64, _ time.Duration) (int64, error) {
	var best int64
	for _, o := range f.orders {
		if o.ProposalID != nil && *o.ProposalID == propID &&
			o.Status != "failed" && o.ID > best {
			best = o.ID
		}
	}
	return best, nil
}
func (f *fakeStore) InsertAlertDedup(_ context.Context, level, msg string, _ time.Duration) error {
	f.alerts = append(f.alerts, level+":"+msg)
	return nil
}
func (f *fakeStore) DCIOrderIntentCount(_ context.Context, prefix string) (int, error) {
	n := 0
	for k := range f.orders {
		if strings.HasPrefix(k, prefix+"|") {
			n++
		}
	}
	return n, nil
}
func (f *fakeStore) DCIOrderOpenIntent(_ context.Context, prefix string) (bool, error) {
	for _, o := range f.orders {
		if strings.HasPrefix(o.RequestID, prefix+"|") &&
			(o.Status == "" || o.Status == "pending" || o.Status == "filled" || o.Status == "settled") {
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeStore) ListVenueRejects(_ context.Context, venue, op string) ([]pgstore.VenueReject, error) {
	var out []pgstore.VenueReject
	for _, p := range f.pits {
		if p.Venue == venue && p.Op == op {
			out = append(out, p)
		}
	}
	return out, nil
}
func (f *fakeStore) LearnVenueReject(_ context.Context, venue, op, sig string, _ time.Duration) error {
	for i := range f.pits {
		if f.pits[i].Venue == venue && f.pits[i].Op == op && f.pits[i].ErrorSignature == sig {
			f.pits[i].Hits++
			return nil
		}
	}
	exp := time.Now().Add(24 * time.Hour)
	f.pits = append(f.pits, pgstore.VenueReject{
		Venue: venue, Op: op, ErrorSignature: sig, Action: "suppress", Hits: 1, ExpiresAt: &exp,
	})
	return nil
}
func (f *fakeStore) InsertAlert(_ context.Context, _ *int64, level, msg string) error {
	f.alerts = append(f.alerts, level+": "+msg)
	return nil
}

// execR3 R3 形态 executor：PriceFn/BalanceFn 注入、Sapi=测试服。
func execR3(fs *fakeStore, url string, cli *http.Client, bals map[string]float64) *Executor {
	return &Executor{ModeGate: liveModeGate, SigmaFn: testSigma, Store: fs, BnKey: "k", BnSec: "s", Sapi: url, Client: cli, Now: time.Now,
		PriceFn: func(context.Context, string) (float64, error) { return 100000, nil },
		BalanceFn: func(_ context.Context, a string) (float64, error) {
			return bals[a], nil
		},
		EarnFn:   func(context.Context, string) ([]EarnPosition, error) { return nil, nil },
		RedeemFn: func(context.Context, string, float64) error { return nil },
	}
}

// TestD191_SignQuery：签名串构造对拍——query 字段齐+HMAC 可验。
func TestD191_SignQuery(t *testing.T) {
	e := &Executor{ModeGate: liveModeGate, SigmaFn: testSigma, BnKey: "k", BnSec: "s", Sapi: "https://x.test", Now: time.Now}
	at := time.UnixMilli(1700000000000)
	q, url := e.SubscribeBody("PROJ1", "ORD1", 0.018, at)
	// R10①：venue 参数面四参对拍（原 projectId/lots 错代）。
	for _, want := range []string{"id=PROJ1", "orderId=ORD1", "depositAmount=0.018", "autoCompoundPlan=NONE"} {
		if !strings.Contains(q, want) {
			t.Fatalf("query 字段缺 %s: %s", want, q)
		}
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
	pid := fs.addProposal("dci_sell_high_covered_call")
	_, err := e.GoProposal(context.Background(), pid)
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
		fs.cons = append(c.cons,
			pgstore.CapabilityConstraint{Subject: "dci_call_otm_k", ValueNum: f64(1.5)},
			pgstore.CapabilityConstraint{Subject: "dci_put_otm_k", ValueNum: f64(1.5)},
			pgstore.CapabilityConstraint{Subject: "dci_tenor_max_days", ValueNum: f64(30)})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if dciListOK(w, r) {
				return
			}
			fmt.Fprint(w, `{"orderId":1}`)
		}))
		e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"BTC": 0.06})
		pid := fs.addProposal("dci_sell_high_covered_call")
		if _, err := e.GoProposal(context.Background(), pid); err != nil {
			t.Fatal(err)
		}
		srv.Close()
		if len(fs.maxTenor) == 0 || fs.maxTenor[0] != c.want {
			t.Fatalf("%s: cap=%v want %d", c.name, fs.maxTenor, c.want)
		}
	}
}

// TestD191R3_StrikeGate：%OTM 闸（D-526 σ 界）——CALL 档 strike 低于 σ 界滤出→
// 无达标档 gap；PUT 档 strike=80000≤σ 界达标成交。
func TestD191R3_StrikeGate(t *testing.T) {
	fs := newFake()
	fs.quotes[0].Strike = f64(100500) // D-526：CALL 档低于 σ 界≈100820→滤出
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		calls++
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"BTC": 0.06, "USDT": 5000})
	pid := fs.addProposal("dci_buy_low")
	msg, err := e.GoProposal(context.Background(), pid)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 { // 仅 PUT 腿 POST
		t.Fatalf("CALL 档应被 OTM 闸滤出: calls=%d", calls)
	}
	if !strings.Contains(msg, "PUT腿申购已提交") {
		t.Fatalf("闸面错: gaps=%+v msg=%s", fs.gaps, msg)
	}
	// 提案腿域=PUT：CALL 档不经评估（D-189 提案腿子集语义）。
	for _, g := range fs.gaps {
		if strings.Contains(g.RequestText, "CALL腿") {
			t.Fatalf("buy_low 提案不应评估 CALL 档: %+v", g)
		}
	}
	// 反转：PUT strike 高于 100000 界（R9=1.00 贴现货）→两腿皆无档。
	fs2 := newFake()
	fs2.quotes[0].Strike = f64(100500) // D-526：低于 σ 界≈100820→CALL 亦无档
	fs2.quotes[1].Strike = f64(101000)
	e2 := execR3(fs2, srv.URL, srv.Client(), map[string]float64{"BTC": 0.06, "USDT": 5000})
	if _, err := e2.GoWheel(context.Background()); err != nil { // 双腿无档全 gap 面用 wheel 径
		t.Fatal(err)
	}
	crit := 0
	for _, a := range fs2.alerts {
		if strings.HasPrefix(a, "critical:") && strings.Contains(a, "系统侧病灶") {
			crit++
		}
	}
	if crit != 2 || len(fs2.orders) != 0 { // D-247：runtime→critical 告警不落请求箱
		t.Fatalf("双腿无档应各发 critical 告警不降级凑单: alerts=%+v orders=%d", fs2.alerts, len(fs2.orders))
	}
}

// TestD191R3_QtyBalanceShare：qty=币安实时余额×custody_share（禁读持仓静态行）。
func TestD191R3_QtyBalanceShare(t *testing.T) {
	fs := newFake()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"BTC": 0.2, "USDT": 10000})
	if _, err := e.GoWheel(context.Background()); err != nil {
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
		if dciListOK(w, r) {
			return
		}
		calls++
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"BTC": 0.2, "USDT": 10000})
	if _, err := e.GoWheel(context.Background()); err != nil {
		t.Fatal(err)
	}
	msg, err := e.GoWheel(context.Background())
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
		if dciListOK(w, r) {
			return
		}
		calls++
		w.WriteHeader(403)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"BTC": 0.2, "USDT": 0})
	// USDT=0→PUT 腿跳过，仅 CALL 腿失败
	if _, err := e.GoWheel(context.Background()); err != nil {
		t.Fatal(err) // GoWheel 双腿汇总不返 err（腿级失败入消息）
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
	found := ""
	for _, g := range fs.gaps {
		if g.Subject == "dual_order" {
			found = g.Kind
		}
	}
	if found != "permission" { // D-196④：HTTP 403→permission 缺口（真业主请求）
		t.Fatalf("403 应落 permission 缺口: %q gaps=%+v", found, fs.gaps)
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
		if dciListOK(w, r) {
			return
		}
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
	e := &Executor{ModeGate: liveModeGate, SigmaFn: testSigma, Store: newFake(), BnKey: "k", BnSec: "s",
		Sapi: srv.URL + "/sapi", APIBase: srv.URL + "/api",
		Client: srv.Client(), Now: time.Now}
	if _, err := e.spotPrice(context.Background(), "BTCUSDT"); err != nil {
		t.Fatal(err)
	}
	bal, err := e.freeBalance(context.Background(), "BTC")
	if err != nil || bal != 0.11 { // free+locked=0.1+0.01
		t.Fatalf("freeBalance: %v %v", bal, err)
	}
	if _, err := e.post(context.Background(), "P1", "O1", 0.01, time.Now()); err != nil {
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
		if dciListOK(w, r) {
			return
		}
		fmt.Fprint(w, sb.String())
	}))
	defer srv.Close()
	e := &Executor{ModeGate: liveModeGate, SigmaFn: testSigma, Store: newFake(), BnKey: "k", BnSec: "s",
		APIBase: srv.URL + "/api", Client: srv.Client(), Now: time.Now}
	bal, err := e.freeBalance(context.Background(), "BTC")
	if err != nil || bal != 0.5 {
		t.Fatalf("大 JSON 应解析出末位 BTC=0.5: %v %v", bal, err)
	}
}

// TestD191R6_PoolEarnAndRedeem：池=现货+赚币子账；现货不足→前置赎回缺口→重读下单。
func TestD191R6_PoolEarnAndRedeem(t *testing.T) {
	fs := newFake()
	var redeems []float64
	spotBals := map[string]float64{"BTC": 0.02}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		if strings.Contains(r.URL.Path, "subscribe") {
			calls++
		}
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e := &Executor{ModeGate: liveModeGate, SigmaFn: testSigma, Store: fs, BnKey: "k", BnSec: "s", Sapi: srv.URL,
		Client: srv.Client(), Now: time.Now,
		PriceFn: func(context.Context, string) (float64, error) { return 100000, nil },
		BalanceFn: func(_ context.Context, a string) (float64, error) {
			return spotBals[a], nil // 现货只 0.02 起
		},
		EarnFn: func(_ context.Context, a string) ([]EarnPosition, error) {
			return []EarnPosition{{ProductID: "EP1", Amount: 0.2}}, nil // 赚币 0.2
		},
		RedeemFn: func(_ context.Context, _ string, amt float64) error {
			redeems = append(redeems, amt)
			spotBals["BTC"] += amt // 赎回到账现货长
			return nil
		}}
	pid := fs.addProposal("dci_sell_high_covered_call")
	if _, err := e.GoProposal(context.Background(), pid); err != nil {
		t.Fatal(err)
	}
	// 池=0.22×0.34=0.0748，现货 0.02<qty→赎回缺口 0.0548。
	if len(redeems) < 1 || math.Abs(redeems[0]-(0.22*0.34-0.02)) > 1e-9 {
		t.Fatalf("CALL 腿赎回缺口错: %v", redeems)
	}
	var o *pgstore.DCIOrder
	for _, oo := range fs.orders {
		if oo.OptType == "CALL" {
			o = oo
		}
	}
	if o == nil || math.Abs(o.Qty-0.22*0.34) > 1e-9 {
		t.Fatalf("qty 应按池=现货+赚币: %+v", o)
	}
	_ = calls
}

// TestD191R6_BlindGate：live≈0&ledger>0→gap 禁缩账（盲源不缩份额行）。
func TestD191R6_BlindGate(t *testing.T) {
	fs := newFake()
	poolQty := 0.05
	fs.positions = []pgstore.PersonalPosition{
		{ID: 7, Asset: "BTC", Kind: "other", Status: "OPEN", Qty: &poolQty},
	}
	// 余额读 0→盲源闸触发（禁缩账）。
	e := execR3(fs, "http://x", nil, map[string]float64{"BTC": 0})
	_, _, _, err := e.ReconcilePoolOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "盲读") {
		t.Fatalf("盲源闸应 gap: %v", err)
	}
	if len(fs.updates) != 0 {
		t.Fatal("盲源禁缩账——不应有修正写")
	}
	found := false
	for _, a := range fs.alerts { // D-247：盲源闸=系统侧病灶→critical 告警不落箱
		if strings.HasPrefix(a, "critical:") && strings.Contains(a, "earn_position") {
			found = true
		}
	}
	if !found {
		t.Fatalf("earn_position runtime critical 未落: %+v", fs.alerts)
	}
}

// TestD192_StreamBalanceGate：freeBalance 读流——流存活+资产有值→流值；
// 死流/资产无值→REST 兜底（闸=连接存活标非事件新旧；边界=现货域）。
func TestD192_StreamBalanceGate(t *testing.T) {
	bus.SpotAssets.Put("binance:spot", "BTC", 0.3, 0.02, time.Now())
	defer func() { // 清面防串测
		bus.SpotAssets = bus.NewAssetLedger()
	}()
	restCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		restCalls++
		fmt.Fprint(w, `{"balances":[{"asset":"BTC","free":"0.5","locked":"0"}]}`)
	}))
	defer srv.Close()
	// 存活+有值→流值 0.32，REST 零调。
	e := &Executor{ModeGate: liveModeGate, SigmaFn: testSigma, Store: newFake(), BnKey: "k", BnSec: "s",
		APIBase: srv.URL + "/api", Client: srv.Client(), Now: time.Now,
		StreamFn: func() bool { return true }}
	if v, err := e.freeBalance(context.Background(), "BTC"); err != nil || v != 0.32 {
		t.Fatalf("流值应 0.32: %v %v", v, err)
	}
	if restCalls != 0 {
		t.Fatalf("活流不应走 REST: %d", restCalls)
	}
	// 流存活但资产无值→REST 兜底。
	if v, err := e.freeBalance(context.Background(), "ETH"); err != nil || v != 0 {
		t.Fatalf("ETH 无值应 REST 兜底（REST 无该行→0）: %v %v", v, err)
	}
	// 死流→REST 兜底。
	e.StreamFn = func() bool { return false }
	if v, err := e.freeBalance(context.Background(), "BTC"); err != nil || v != 0.5 {
		t.Fatalf("死流应 REST=0.5: %v %v", v, err)
	}
}

// TestD191R8_DeepPutSelectable：高 apy 近平值档被界滤、深档选得中（序错修后
// ListDCIQuotes 收到腿闸界——fake 同语义过滤，PUTP strike 80000 中标）。
func TestD191R8_DeepPutSelectable(t *testing.T) {
	fs := newFake()
	tenor := 7
	// 追加高 apy 近平值 PUT（strike 105000>100000 界——R9 PUT 界=1.00 贴现货）。
	fs.quotes = append(fs.quotes, pgstore.CexDCIQuote{
		Venue: "binance", Underlying: "BTC", InvestCcy: "USDT",
		Strike: f64(105000), OptType: "PUT", TenorDays: &tenor, APY: 0.50,
		ProductRef: "NEARP", OrderRef: "ON1", PurchaseDecimal: idec(4)})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"USDT": 10000})
	pid := fs.addProposal("dci_buy_low")
	if _, err := e.GoProposal(context.Background(), pid); err != nil {
		t.Fatal(err)
	}
	var o *pgstore.DCIOrder
	for _, oo := range fs.orders {
		o = oo
	}
	if o == nil || o.ProductRef != "PUTP" {
		t.Fatalf("深档应选得中: %+v", o)
	}
}

// TestD191R9_BoundFromConstraint：OTM 界读约束表——界行缺→constraint 缺口拒单
// （同 custody_share 例，不回退字面量兜底）。
func TestD191R9_BoundFromConstraint(t *testing.T) {
	fs := newFake()
	// 摘 PUT σ 界 k 行→PUT 腿缺口拒、CALL 腿正常（CALL k 仍在）。
	var cons []pgstore.CapabilityConstraint
	for _, c := range fs.cons {
		if c.Subject != "dci_put_otm_k" {
			cons = append(cons, c)
		}
	}
	fs.cons = cons
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"USDT": 10000})
	pid := fs.addProposal("dci_buy_low")
	_, err := e.GoProposal(context.Background(), pid)
	if err == nil {
		t.Fatal("缺界应拒单")
	}
	found := false
	for _, g := range fs.gaps {
		if g.Subject == "dci_put_otm_k" && g.Kind == "constraint" {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺界未落 constraint 缺口: %+v", fs.gaps)
	}
}

// TestD191R10_OrderRefSkip：order_ref 缺行跳次档、全缺→「无可申购参数档」缺口。
func TestD191R10_OrderRefSkip(t *testing.T) {
	fs := newFake()
	fs.quotes[1].OrderRef = ""   // PUTP 缺 order_ref
	fs.quotes[1].MinAmount = nil // +缺 min_amount（R12 跳档谓词换锚）
	tenor := 7
	fs.quotes = append(fs.quotes, pgstore.CexDCIQuote{
		Venue: "binance", Underlying: "BTC", InvestCcy: "USDT",
		Strike: f64(70000), OptType: "PUT", TenorDays: &tenor, APY: 0.05,
		ProductRef: "PUTP2", OrderRef: "OP2", PurchaseDecimal: idec(0), MinAmount: f64(1)}) // 次档有 orderRef→跳此成交
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"USDT": 10000})
	pid := fs.addProposal("dci_buy_low")
	if _, err := e.GoProposal(context.Background(), pid); err != nil {
		t.Fatal(err)
	}
	var o *pgstore.DCIOrder
	for _, oo := range fs.orders {
		o = oo
	}
	if o == nil || o.ProductRef != "PUTP2" {
		t.Fatalf("order_ref 缺行应跳次档: %+v", o)
	}
	// 全缺→「无可申购参数档」如实报。
	fs2 := newFake()
	for i := range fs2.quotes {
		fs2.quotes[i].OrderRef = ""
	}
	e2 := execR3(fs2, srv.URL, srv.Client(), map[string]float64{"USDT": 10000})
	pid2 := fs2.addProposal("dci_buy_low")
	_, err := e2.GoProposal(context.Background(), pid2)
	if err == nil || !strings.Contains(err.Error(), "无可申购参数档") {
		t.Fatalf("全缺应报无可申购参数档: %v gaps=%+v", err, fs2.gaps)
	}
	// msgs 内嵌 err：venue 原文拒绝体随回执直发。
	if !strings.Contains(err.Error(), "PUT腿") {
		t.Fatalf("err 未内嵌腿因 msgs: %v", err)
	}
}

// TestD196_VenueErrKind：venue 败因分类——400/参数→runtime、401/403/权限码→permission。
func TestD196_VenueErrKind(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{"subscribe HTTP 400: {\"code\":-1102,\"msg\":\"Mandatory parameter 'id' was not sent\"}", "runtime"},
		{"subscribe HTTP 403: {\"code\":-2015,\"msg\":\"Invalid API-key\"}", "permission"},
		{"subscribe HTTP 401: {}", "permission"},
		{"dial tcp: connection refused", "runtime"},
	}
	for _, c := range cases {
		if got := venueErrKind(fmt.Errorf("%s", c.body)); got != c.want {
			t.Fatalf("%q → %s want %s", c.body, got, c.want)
		}
	}
	// 全链：400 拒参→runtime 缺口「系统侧病灶」非「请」。
	fs := newFake()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		w.WriteHeader(400)
		fmt.Fprint(w, `{"code":-1102,"msg":"Mandatory parameter was not sent"}`)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"BTC": 0.2, "USDT": 0})
	if _, err := e.GoWheel(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, a := range fs.alerts {
		if strings.HasPrefix(a, "critical:") && strings.Contains(a, "dual_order") &&
			strings.Contains(a, "系统侧病灶") {
			return // D-247：400 拒参→critical 告警不落请求箱
		}
	}
	t.Fatalf("dual_order critical 未落: gaps=%+v alerts=%+v", fs.gaps, fs.alerts)
}

// TestD196R1_SpotErrClass：spotPrice 读败分面——HTTP 400→runtime 不落
// permission（复审退修：GoProposal/GoWheel 两处漏面照 tryLeg 同款收）。
func TestD196R1_SpotErrClass(t *testing.T) {
	for _, want := range []struct {
		name string
		err  error
		kind string
	}{
		{"400→runtime", fmt.Errorf("ticker HTTP 400: {\"code\":-1100}"), "runtime"},
		{"403→permission", fmt.Errorf("ticker HTTP 403: forbidden"), "permission"},
	} {
		fs := newFake()
		e := execR3(fs, "", nil, map[string]float64{"BTC": 0.2, "USDT": 0})
		e.PriceFn = func(ctx context.Context, symbol string) (float64, error) { return 0, want.err }
		pid := fs.addProposal("dci_buy_low")
		if _, err := e.GoProposal(context.Background(), pid); err == nil {
			t.Fatalf("%s: GoProposal 应返错", want.name)
		}
		if want.kind == "runtime" { // D-247：runtime→critical 告警不落箱
			found := false
			for _, a := range fs.alerts {
				if strings.HasPrefix(a, "critical:") && strings.Contains(a, "dual_quote") {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: dual_quote critical 未落 alerts=%+v", want.name, fs.alerts)
			}
			continue
		}
		found := ""
		for _, g := range fs.gaps {
			if g.Subject == "dual_quote" {
				found = g.Kind
			}
		}
		if found != want.kind {
			t.Fatalf("%s: dual_quote kind=%q want %q gaps=%+v", want.name, found, want.kind, fs.gaps)
		}
	}
}

// TestD191R11_AmountBoundLease：①floor dec 格式对拍；②界外 runtime 如实败；
// ③120s 租约二发零 venue 调用；④runtime err 无双前缀。
func TestD191R11_AmountBoundLease(t *testing.T) {
	e := &Executor{ModeGate: liveModeGate, SigmaFn: testSigma, BnKey: "k", BnSec: "s", Sapi: "https://x.test", Now: time.Now}
	at := time.UnixMilli(1700000000000)
	q, _ := e.SubscribeBody("P1", "O1", 541.0, at)
	if !strings.Contains(q, "depositAmount=541&") { // dec0 floor→"541" 非 "541.00000000"
		t.Fatalf("depositAmount 格式错: %s", q)
	}
	if floorTo(0.0748, 4) != 0.0748 || floorTo(0.07489, 4) != 0.0748 {
		t.Fatal("floorTo 错")
	}
	// R12：floor 位真源=min_amount 小数位（USDT min=10→dec0、BTC min=0.0001→dec4）。
	if decOf(10) != 0 || decOf(0.0001) != 4 {
		t.Fatalf("decOf 错: %d %d", decOf(10), decOf(0.0001))
	}
	dep := floorTo(541.9, decOf(10))
	if strconv.FormatFloat(dep, 'f', -1, 64) != "541" {
		t.Fatalf("min10→应发 541: %v", dep)
	}
	// 界外：PUT 档 max=10 < deposit 3400 → runtime 界外缺口+零 POST。
	fs := newFake()
	*fs.quotes[1].MaxAmount = 10
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		calls++
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	ex := execR3(fs, srv.URL, srv.Client(), map[string]float64{"USDT": 10000})
	_, err := ex.GoProposal(context.Background(), fs.addProposal("dci_buy_low"))
	if err == nil || !strings.Contains(err.Error(), "界外") {
		t.Fatalf("界外应 runtime 如实败: %v", err)
	}
	if calls != 0 {
		t.Fatalf("界外档禁发单, calls=%d", calls)
	}
	if strings.Count(err.Error(), "系统侧病灶") != 1 {
		t.Fatalf("runtime 文案双前缀: %v", err)
	}
	// 租约：首发成交→120s 内二发零 venue 调用拒「刚执行过（订单#M）」。
	fs2 := newFake()
	calls2 := 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		calls2++
		fmt.Fprint(w, `{"orderId":9}`)
	}))
	defer srv2.Close()
	ex2 := execR3(fs2, srv2.URL, srv2.Client(), map[string]float64{"USDT": 10000})
	pid := fs2.addProposal("dci_buy_low")
	if _, err := ex2.GoProposal(context.Background(), pid); err != nil {
		t.Fatal(err)
	}
	if calls2 != 1 {
		t.Fatalf("首发应成交, calls=%d", calls2)
	}
	// 提案回 proposed 模拟重复口令（executed 态机另挡——租约防的是状态缝双击）。
	fs2.proposals[pid].Status = "proposed"
	_, err = ex2.GoProposal(context.Background(), pid)
	if err == nil || !strings.Contains(err.Error(), "刚执行过（订单#1）") {
		t.Fatalf("租约应拒单: %v", err)
	}
	if calls2 != 1 {
		t.Fatalf("窗内二发应零 venue 调用, calls=%d", calls2)
	}
}

// TestD191R14_OrderRefRefresh：申购前实时重取 orderId+canPurchase——
// refresh 类拒因一次再刷新重提成功；下架→runtime gap；二败如实 gap。
func TestD191R14_OrderRefRefresh(t *testing.T) {
	// ①过期→刷新→重提成功：首 POST refresh 败、再取 orderId 重提成。
	fs := newFake()
	posts := 0
	var gotOrderID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		posts++
		gotOrderID = r.URL.Query().Get("orderId")
		if posts == 1 {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"code":-1000,"msg":"please refresh orderId"}`)
			return
		}
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"USDT": 10000})
	if _, err := e.GoProposal(context.Background(), fs.addProposal("dci_buy_low")); err != nil {
		t.Fatal(err)
	}
	if posts != 2 {
		t.Fatalf("refresh 类错应一次再刷新重提, posts=%d", posts)
	}
	if gotOrderID != "8822" { // 重取新 orderId 发单（DB OP1 降格候选证据）
		t.Fatalf("应发实时 orderId: %s", gotOrderID)
	}
	// ②下架→「产品已下架」runtime gap 零 POST。
	fs2 := newFake()
	posts2 := 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "product/list") {
			fmt.Fprint(w, `{"list":[{"id":"OTHER","orderId":9,"canPurchase":true}]}`)
			return
		}
		posts2++
	}))
	defer srv2.Close()
	e2 := execR3(fs2, srv2.URL, srv2.Client(), map[string]float64{"USDT": 10000})
	_, err := e2.GoProposal(context.Background(), fs2.addProposal("dci_buy_low"))
	if err == nil || !strings.Contains(err.Error(), "产品已下架") {
		t.Fatalf("下架应 runtime gap: %v", err)
	}
	if posts2 != 0 {
		t.Fatalf("下架禁发单, posts=%d", posts2)
	}
	// ③二败如实 gap：两发都 refresh 错→不再重试如实败。
	fs3 := newFake()
	posts3 := 0
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		posts3++
		w.WriteHeader(400)
		fmt.Fprint(w, `{"code":-1000,"msg":"please refresh orderId"}`)
	}))
	defer srv3.Close()
	e3 := execR3(fs3, srv3.URL, srv3.Client(), map[string]float64{"USDT": 10000})
	_, err = e3.GoProposal(context.Background(), fs3.addProposal("dci_buy_low"))
	if err == nil || !strings.Contains(err.Error(), "refresh") {
		t.Fatalf("二败应如实 gap: %v", err)
	}
	if posts3 != 2 {
		t.Fatalf("refresh 类只重提一次, posts=%d", posts3)
	}
}

// TestD198_Resume：断点续执五锚——failed 提案续执/无单提案不触发/executed 不触发/
// 已锁仓标 executed 零重购/outstanding 读败跳过+runtime gap。
func TestD198_Resume(t *testing.T) {
	mkFailed := func(fs *fakeStore, pid int64, id int64) {
		fs.orders[fmt.Sprintf("req%d", id)] = &pgstore.DCIOrder{
			ID: id, ProposalID: &pid, ProductRef: "PUTP", OptType: "PUT", InvestCcy: "USDT", Status: "failed"}
	}
	// ①failed 提案 boot 续执→走 GoProposal 同径成交。
	fs := newFake()
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if dciListOK(w, r) {
			return
		}
		posts++
		fmt.Fprint(w, `{"orderId":1}`)
	}))
	defer srv.Close()
	e := execR3(fs, srv.URL, srv.Client(), map[string]float64{"USDT": 10000})
	pid := fs.addProposal("dci_buy_low")
	mkFailed(fs, pid, 1)
	n, err := e.ResumeFailedProposals(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("续执: n=%d err=%v", n, err)
	}
	if posts != 1 {
		t.Fatalf("续执应下单一次, posts=%d", posts)
	}
	// 成交+续执回执 warn 级（info 滤噪闸会拦——回执必达飞书）。
	var warnHit int
	for _, a := range fs.alerts {
		if strings.HasPrefix(a, "warn:") && (strings.Contains(a, "申购成交") || strings.Contains(a, "断点续执")) {
			warnHit++
		}
	}
	if warnHit < 2 {
		t.Fatalf("成交+续执回执应 warn 级必达: %+v", fs.alerts)
	}
	// 返修②：败单行存在下续执真发 venue——reqID 尾尝试序 |2 不再 ON CONFLICT 吞。
	var newReq string
	for rid := range fs.orders {
		if rid != "req1" {
			newReq = rid
		}
	}
	if !strings.HasSuffix(newReq, "|2") {
		t.Fatalf("reqID 应带尝试序 |2: %q", newReq)
	}
	// 撞 n 并发仍 dedup：同 reqID 二登→created=false。
	if _, dup, _ := fs.CreateDCIOrder(context.Background(), &pgstore.DCIOrder{RequestID: newReq}); dup {
		t.Fatal("同 reqID 重登应 dedup")
	}
	// ②无订单 proposed 提案不触发。
	fs2 := newFake()
	fs2.addProposal("dci_buy_low")
	if n, _ := execR3(fs2, srv.URL, srv.Client(), nil).ResumeFailedProposals(context.Background()); n != 0 {
		t.Fatalf("无单提案不应触发: %d", n)
	}
	// ③executed 提案不触发。
	fs3 := newFake()
	pid3 := fs3.addProposal("dci_buy_low")
	fs3.proposals[pid3].Status = "executed"
	mkFailed(fs3, pid3, 9)
	if n, _ := execR3(fs3, srv.URL, srv.Client(), nil).ResumeFailedProposals(context.Background()); n != 0 {
		t.Fatalf("executed 提案不应触发: %d", n)
	}
	// ④venue 持仓存在→executed 零重购（venue 侧读径主，本地 filled 降为补充）。
	fs4 := newFake()
	pid4 := fs4.addProposal("dci_buy_low")
	mkFailed(fs4, pid4, 1)
	posts4 := 0
	srv4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "product/positions") {
			fmt.Fprint(w, `{"data":[{"id":"PUTP","optionType":"PUT","status":"HOLDING"}]}`)
			return
		}
		if dciListOK(w, r) {
			return
		}
		posts4++
	}))
	defer srv4.Close()
	e4 := execR3(fs4, srv4.URL, srv4.Client(), map[string]float64{"USDT": 10000})
	if n, err := e4.ResumeFailedProposals(context.Background()); err != nil || n != 0 {
		t.Fatalf("已锁仓不应续执: n=%d err=%v", n, err)
	}
	if fs4.proposals[pid4].Status != "executed" {
		t.Fatalf("已锁仓应标 executed: %s", fs4.proposals[pid4].Status)
	}
	if posts4 != 0 {
		t.Fatalf("已锁仓零重购, posts=%d", posts4)
	}
	// ④b 本地 filled 补充（venue 空仓+本地 filled 行→同标 executed）。
	fs4b := newFake()
	pid4b := fs4b.addProposal("dci_buy_low")
	mkFailed(fs4b, pid4b, 1)
	fs4b.orders["reqF"] = &pgstore.DCIOrder{ID: 2, ProductRef: "PUTP", OptType: "PUT", Status: "filled"}
	if n, _ := execR3(fs4b, srv.URL, srv.Client(), nil).ResumeFailedProposals(context.Background()); n != 0 ||
		fs4b.proposals[pid4b].Status != "executed" {
		t.Fatalf("本地 filled 补充应标 executed: n=%d st=%s", n, fs4b.proposals[pid4b].Status)
	}
	// ⑤venue 持仓读败→跳过+runtime gap 不重发。
	fs5 := newFake()
	pid5 := fs5.addProposal("dci_buy_low")
	mkFailed(fs5, pid5, 1)
	posts5 := 0
	srv5 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "product/positions") {
			w.WriteHeader(500)
			return
		}
		if dciListOK(w, r) {
			return
		}
		posts5++
	}))
	defer srv5.Close()
	e5 := execR3(fs5, srv5.URL, srv5.Client(), map[string]float64{"USDT": 10000})
	if n, _ := e5.ResumeFailedProposals(context.Background()); n != 0 {
		t.Fatalf("读败应跳过: %d", n)
	}
	if posts5 != 0 {
		t.Fatalf("读败不重发, posts=%d", posts5)
	}
	found := false
	for _, a := range fs5.alerts { // D-247：runtime→critical 告警不落箱
		if strings.HasPrefix(a, "critical:") && strings.Contains(a, "读败") {
			found = true
		}
	}
	if !found {
		t.Fatalf("读败应发 critical 告警: %+v", fs5.alerts)
	}
}

// ---- D-207 交割观察面 fake ----
func (f *fakeStore) ListFilledDCIOrders(context.Context) ([]pgstore.FilledDCIOrder, error) {
	return f.filledOrders, nil
}
func (f *fakeStore) SettleDCIOrder(_ context.Context, id int64, ccy string, qty, income float64) (bool, error) {
	f.settled = append(f.settled, struct {
		id          int64
		ccy         string
		qty, income float64
	}{id, ccy, qty, income})
	return true, nil
}
func (f *fakeStore) ClosePersonalPositionsByMarker(_ context.Context, marker, suffix string) (int64, error) {
	f.closedMarkers = append(f.closedMarkers, marker)
	if f.closeN != nil {
		return *f.closeN, nil
	}
	return 1, nil
}
func (f *fakeStore) ExchangeConfigID(context.Context, string, string) (int64, error) {
	return f.cfgID, nil
}
func (f *fakeStore) InsertIncomeEvents(_ context.Context, evs []pgstore.IncomeEvent) (int64, error) {
	f.incomeEvs = append(f.incomeEvs, evs...)
	return int64(len(evs)), nil
}
func (f *fakeStore) AppConfigInt64(context.Context, string) (int64, bool, error) {
	return 0, false, nil
}

func (f *fakeStore) AppConfigFloat(_ context.Context, key string) (float64, bool, error) {
	if f.cfgF != nil {
		if v, ok := f.cfgF[key]; ok {
			return v, true, nil
		}
	}
	return 0, false, nil
}

func (f *fakeStore) LatestEarnActivityAt(_ context.Context, _ string) (time.Time, bool, error) {
	return f.lastEarnAt, f.hasEarnAt, nil
}

func (f *fakeStore) ListPositionLegs(_ context.Context, _ int64) ([]pgstore.PositionLeg, error) {
	return f.legs, nil
}

func (f *fakeStore) JitNetOutstanding(_ context.Context, _ string) (float64, error) {
	return f.jitOutstanding, nil
}

// TestM41_EvalGate 评估卡闸（D-258 M4.1③）：无卡/非 viable 拒单，viable 过闸。
func TestM41_EvalGate(t *testing.T) {
	st := newFake()
	e := &Executor{ModeGate: liveModeGate, SigmaFn: testSigma, Store: st, BnKey: "k", BnSec: "s", Now: time.Now}
	pid := st.addProposal("dci_buy_low")

	delete(st.evals, pid)
	if _, err := e.GoProposal(context.Background(), pid); err == nil ||
		!strings.Contains(err.Error(), "无评估卡") {
		t.Fatalf("无卡应拒: %v", err)
	}
	st.evals[pid] = &pgstore.OppEval{ProposalID: pid, Verdict: "unevaluable"}
	if _, err := e.GoProposal(context.Background(), pid); err == nil ||
		!strings.Contains(err.Error(), "评估卡=unevaluable") {
		t.Fatalf("unevaluable 应拒: %v", err)
	}
	st.evals[pid] = &pgstore.OppEval{ProposalID: pid, Verdict: "reject"}
	if _, err := e.GoProposal(context.Background(), pid); err == nil ||
		!strings.Contains(err.Error(), "评估卡=reject") {
		t.Fatalf("reject 应拒: %v", err)
	}
	// viable 过卡闸——下一道「无执行轨模板」拦验证非卡因（dci_buy_low 有轨，
	// 走到 venue 调用面才败——断言错误非卡文案）。
	st.evals[pid] = &pgstore.OppEval{ProposalID: pid, Verdict: "viable"}
	_, err := e.GoProposal(context.Background(), pid)
	if err != nil && strings.Contains(err.Error(), "评估卡") {
		t.Fatalf("viable 不应卡闸拒: %v", err)
	}
}

// D-593 桩：纸面闸默认放行（测试路径不判模板——闸生效验在 pgstore 锚）；
// paperGateBlock 非空=拒（TestD593_GateWired 接线锚专用）。
func (f *fakeStore) PaperFirstGate(_ context.Context, _ string, _ func() (bool, error)) (bool, string, error) {
	if f.paperGateBlock != "" {
		return false, f.paperGateBlock, nil
	}
	return true, "", nil
}
func (f *fakeStore) HasPaperEvidence(context.Context, string) (bool, error) { return true, nil }

// TestD593_GateWired 接线锚（复审⑧修件）：闸拒=原本可推进的提案被拦——
// 删 exec.go 闸调用则本测红（闸存在≠接线，防三轮「函数在无人调」病灶再犯）。
func TestD593_GateWired(t *testing.T) {
	fs := newFake()
	e := execR3(fs, "http://x", nil, map[string]float64{"BTC": 0.06})
	pid := fs.addProposal("dci_sell_high_covered_call")
	fs.paperGateBlock = "无纸面证据且无豁免"
	_, err := e.GoProposal(context.Background(), pid)
	if err == nil || !strings.Contains(err.Error(), "纸面先行闸拒") {
		t.Fatalf("闸拒必拦执行径: %v", err)
	}
	if fs.proposals[pid].Status == "executed" {
		t.Fatal("闸拒后提案不得进 executed")
	}
}

// D-596 桩：io 划转记账捕参（InsertVenueIO 调用断言面）。
var fakeIOCaptures []pgstore.VenueIO

func (f *fakeStore) InsertVenueIO(_ context.Context, rows []pgstore.VenueIO) (int64, error) {
	fakeIOCaptures = append(fakeIOCaptures, rows...)
	return int64(len(rows)), nil
}
