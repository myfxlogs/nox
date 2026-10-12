// D-178 锚：静默 venue 权益证据续命——机审权益闸 stale/缺分支先 RefreshAccountEquity
// 定向回读复判一次：回读成功→判绿；仍超龄/缺→原硬伤/歧义；回读恰一次幂等计数。
package dashboard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/ledger/reconcile"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

// d178Querier 静态查询器：计数+可控成败（gate venue 端到端锚的按需回读源）。
type d178Querier struct {
	vk    string
	calls int
	eq    float64
	fail  bool
}

func (q *d178Querier) VenueKey() string { return q.vk }
func (q *d178Querier) Account(context.Context) (*reconcile.AccountInfo, error) {
	q.calls++
	if q.fail {
		return nil, errors.New("venue rpc timeout")
	}
	return &reconcile.AccountInfo{VenueKey: q.vk, Equity: q.eq, Balance: q.eq, Currency: "USDT", Simulated: false}, nil
}

// d178SvcWithMon：svc + 注入 acctMon（陈旧 gate 态 seed）+ 计数 querier。
func d178SvcWithMon(t *testing.T, stale time.Duration, q *d178Querier) (*Service, *pgstore.Store) {
	t.Helper()
	svc, st := d163Svc(t)
	mon := NewAccountMonitor(NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute), nil)
	mon.AddStatic(q)
	svc.acctMon = mon
	// 种陈旧态（静默 venue asOf 冻结=D-177 实证形态）。
	mon.mu.Lock()
	mon.states[q.vk] = acctVenueState{
		online: true, restOnline: true,
		acct: &reconcile.AccountInfo{VenueKey: q.vk, Equity: q.eq, Currency: "USDT"},
		asOf: time.Now().Add(-stale),
	}
	mon.mu.Unlock()
	return svc, st
}

func d178Proposal(vk string) *pgstore.ProbeProposal {
	return &pgstore.ProbeProposal{Contract: pgstore.ProbeContract{
		VenueKey: vk, Canonical: "CAPUSDT", ExpectedCostUSD: 5,
	}}
}

// gate 端到端：asOf 超龄 + 回读成功 → 判绿（note 非硬伤），回读恰一次。
func TestD178_StaleRefreshGreen(t *testing.T) {
	q := &d178Querier{vk: "gate:perp", eq: 100}
	svc, _ := d178SvcWithMon(t, 10*time.Minute, q)
	reject, ambiguous, note := svc.replayIdleFunds(context.Background(), d178Proposal("gate:perp"), pgstore.ProbePremise{Kind: "idle_funds", Venue: "gate", FreeUSD: 50})
	if q.calls != 1 {
		t.Fatalf("回读应恰一次, calls=%d", q.calls)
	}
	if reject != "" || ambiguous != "" {
		t.Fatalf("回读后应判绿, reject=%q ambiguous=%q", reject, ambiguous)
	}
	if !strings.Contains(note, "eq=") {
		t.Fatalf("note 应携权益值, got %q", note)
	}
}

// 回读失败（REST 超时）：restOnline 翻 false → 复判 ok=false → 走原「快照缺」歧义径
// （票面钦定：仍超龄/缺才判硬伤/歧义，回读失败不新增告警面）。
func TestD178_RefreshFailGoesAmbiguous(t *testing.T) {
	q := &d178Querier{vk: "gate:perp", eq: 100, fail: true}
	svc, _ := d178SvcWithMon(t, 10*time.Minute, q)
	reject, ambiguous, _ := svc.replayIdleFunds(context.Background(), d178Proposal("gate:perp"), pgstore.ProbePremise{Kind: "idle_funds", Venue: "gate"})
	// D-206①：逐候选各至多刷一次——gate:perp 直刷 + 末位裸 gate 全 venue 扫各一次。
	if q.calls != 2 {
		t.Fatalf("回读应恰两次（perp 直刷+gate 裸扫）, calls=%d", q.calls)
	}
	// D-206②：败径分文案——轮询败（querier 未就绪/凭据/超时）非「陈旧/快照缺」。
	if !strings.Contains(ambiguous, "轮询败") {
		t.Fatalf("回读失败应报轮询败, reject=%q ambiguous=%q", reject, ambiguous)
	}
}

// 幂等计数：同一提案两次复判各恰一次回读（跨调用不共享、不循环重试）。
func TestD178_RefreshOncePerAdjudication(t *testing.T) {
	q := &d178Querier{vk: "gate:perp", eq: 100}
	svc, _ := d178SvcWithMon(t, 10*time.Minute, q)
	p := d178Proposal("gate:perp")
	prem := pgstore.ProbePremise{Kind: "idle_funds", Venue: "gate"}
	svc.replayIdleFunds(context.Background(), p, prem)
	if q.calls != 1 {
		t.Fatalf("首轮回读应恰一次, calls=%d", q.calls)
	}
	// 置回陈旧态再判一轮：仍恰一次（证明是「每裁决一次」非缓存/非循环）。
	svc.acctMon.mu.Lock()
	st := svc.acctMon.states[q.vk]
	st.asOf = time.Now().Add(-10 * time.Minute)
	svc.acctMon.states[q.vk] = st
	svc.acctMon.mu.Unlock()
	svc.replayIdleFunds(context.Background(), p, prem)
	if q.calls != 2 {
		t.Fatalf("次轮回读应恰一次, calls=%d", q.calls)
	}
}

// 状态缺席（从未采到）：缺→回读成功→判绿；缺+回读失败→快照缺歧义。
func TestD178_MissingRefresh(t *testing.T) {
	q := &d178Querier{vk: "gate:perp", eq: 100}
	svc, st := d163Svc(t)
	_ = st
	mon := NewAccountMonitor(NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute), nil)
	mon.AddStatic(q)
	svc.acctMon = mon
	reject, ambiguous, note := svc.replayIdleFunds(context.Background(), d178Proposal("gate:perp"), pgstore.ProbePremise{Kind: "idle_funds", Venue: "gate"})
	if q.calls != 1 || reject != "" || ambiguous != "" || !strings.Contains(note, "eq=") {
		t.Fatalf("缺+回读成功应判绿, calls=%d r=%q a=%q n=%q", q.calls, reject, ambiguous, note)
	}
}

// D-206①：腿型幻影键场景——contract vk=gate:mt5（无 querier 匹配）曾吞全局单发
// refresh 致真存在的 gate:perp 永不得刷；逐候选一次后真候选得刷判绿。
func TestD206_PhantomKeyRealCandRefreshes(t *testing.T) {
	q := &d178Querier{vk: "gate:perp", eq: 100}
	svc, _ := d178SvcWithMon(t, 10*time.Minute, q) // gate:perp 态陈旧
	reject, ambiguous, note := svc.replayIdleFunds(context.Background(),
		d178Proposal("gate:mt5"), // 腿型=mt5→幻影首候选
		pgstore.ProbePremise{Kind: "idle_funds", Venue: "gate", FreeUSD: 50})
	if reject != "" || ambiguous != "" {
		t.Fatalf("幻影键场景真候选应判绿, reject=%q ambiguous=%q", reject, ambiguous)
	}
	if !strings.Contains(note, "gate:perp") {
		t.Fatalf("佐证应落 gate:perp: %q", note)
	}
	if q.calls != 1 {
		t.Fatalf("真候选回读应恰一次（幻影键零 querier 零调用）, calls=%d", q.calls)
	}
}
