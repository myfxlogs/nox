// d071_acct_kick_test.go —— D-071 A 锚：margin 首采定向 kick 无防抖直读。
// 生产事故链（10-05 实拍）：Run boot poll 早于 MT5 attach（querier 缺席 → 零账户态），
// margin 首采 kick 若走 Kick("") 全量防抖路径（距 boot <30s）直接被丢，空仓 MT5
// 账户此后零帧零 kick → ListAccounts/EquityObserved 永缺（11:01Z 两 MT5 账户不在列）。
// 修=margin sink 携 venueKey 定向 kick → pollVenueKeys 无防抖直读落态；
// Kick("") 全量防抖保留（REST 风暴保护不松）。
package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/reconcile"
)

func TestD071_MarginFirstPollTargetedKick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	m := NewAccountMonitor(bm, bus.NewTopic[*v1.AccountEvent]())

	// boot poll 时 MT5 querier 尚未 attach（生产 09:58 形态：只 CEX querier ×8）。
	go m.Run(ctx)
	waitFor(t, 2*time.Second, func() bool {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return !m.lastPoll.IsZero() // boot poll 完成、30s 防抖基准已设
	})

	// MT5 attach 晚到：注册查询器，此刻距 boot poll <30s。
	q := &countingAcctQuerier{fakeAccountQuerier: fakeAccountQuerier{
		key: "vA:mt5",
		acct: &reconcile.AccountInfo{
			VenueKey: "vA:mt5", Venue: "vA", VenueType: "mt5", AssetType: "cfd",
			Balance: 2000, Equity: 2000, Currency: "USD",
		},
	}}
	m.AddStatic(q)

	// margin 首采 → 定向 kick：无防抖直读必须采到（旧 "" 全量路径此处被丢=bug）。
	m.Kick("vA:mt5")
	waitFor(t, 2*time.Second, func() bool { return q.calls.Load() == 1 })
	if _, _, _, _, ok := m.EquityObserved("vA:mt5"); !ok {
		t.Fatal("定向 kick 后账户应落态（EquityObserved ok=false=生产缺口形态）")
	}

	// 对照：全量 Kick("") 仍在 30s 防抖窗内被丢——REST 风暴保护不松。
	m.Kick("")
	time.Sleep(300 * time.Millisecond)
	if got := q.calls.Load(); got != 1 {
		t.Fatalf("Kick(\"\") 应被 30s 防抖丢弃，calls=%d want 1", got)
	}
}
