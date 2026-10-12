// d141_floating_test.go D-141 验收锚：「当前浮动」=Σ 平仓净估（全平到手口径）——
// 多仓求和正确 / 含 NULL 仓→available=false / 游离腿计入 / 全 NULL→「—」。
package dashboard

import (
	"context"
	"math"
	"testing"
	"time"

	"strconv"

	"arb/internal/platform/testutil/storetest"

	v1 "arb/internal/gen/dashboardv1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestD141_FloatingCloseNowNet(t *testing.T) {
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	_, _ = st.Pool().Exec(ctx, `DELETE FROM position_legs WHERE position_id IN (SELECT id FROM positions WHERE opportunity_id LIKE 'D-141%')`)
	_, _ = st.Pool().Exec(ctx, `DELETE FROM positions WHERE opportunity_id LIKE 'D-141%'`)
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM position_legs WHERE position_id IN (SELECT id FROM positions WHERE opportunity_id LIKE 'D-141%')`)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM positions WHERE opportunity_id LIKE 'D-141%'`)
	})

	seed := func(oppID string) int64 {
		var id int64
		if err := st.Pool().QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1,'SWAPARB','OPEN',now()) RETURNING id`, oppID).Scan(&id); err != nil {
			t.Fatalf("seed position: %v", err)
		}
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, price_actual, status, order_id)
			 VALUES ($1,1,'OPEN','52993526','mt5','AUDJPYm','BUY',1,113.5,113.5,'FILLED',$2)`, id, "tk-"+itoa64(id)); err != nil {
			t.Fatalf("seed leg: %v", err)
		}
		return id
	}
	posA := seed("D-141-a")
	posB := seed("D-141-b")

	// broker 快照：posA/posB 各一腿 + 游离外单腿（spot cs=1 免 resolver）。
	orphan := &v1.BrokerPosition{
		VenueKey: "binance:spot", Symbol: "SOLUSDT", Ticket: "sol-t", Side: "BUY",
		Fresh: true, OpenPrice: 100, CurrentPrice: 110, Volume: 2,
		OpenedAt: timestamppb.New(time.Now().Add(-time.Hour)),
	}
	newSvc := func(extra ...*v1.BrokerPosition) *Service {
		bps := []*v1.BrokerPosition{
			{VenueKey: "52993526:mt5", Symbol: "AUDJPYm", Ticket: "tk-" + itoa64(posA), Pnl: -40, Swap: 4.99, Commission: -1},
			{VenueKey: "52993526:mt5", Symbol: "AUDJPYm", Ticket: "tk-" + itoa64(posB), Pnl: 10, Swap: 0.01, Commission: 0},
		}
		bps = append(bps, extra...)
		return &Service{store: st, bpm: fakeBPMWithCache(bps)}
	}

	// 锚1：两仓求和 + 游离腿计入。posA close=−40+4.99−1=−36.01；posB close=10+0.01=10.01；
	// 游离腿 est=(110−100)×2×1×1 − 费(110×2×0/1e4=0) + income(无→excluded=含息缺标但值照出)=20。
	sum, n, complete := newSvc(orphan).floatingCloseNowNet(ctx)
	if !complete || n != 3 {
		t.Fatalf("sum path: n=%d complete=%v want 3/true", n, complete)
	}
	if math.Abs(sum-(-36.01+10.01+20)) > 0.01 {
		t.Errorf("sum=%.4f want −6.00", sum)
	}

	// 锚2：含 NULL 仓 → complete=false。posB 腿从快照摘掉 → unmatched 且 funding=0 → CloseNowNetUsd 缺省。
	s2 := &Service{store: st, bpm: fakeBPMWithCache([]*v1.BrokerPosition{
		{VenueKey: "52993526:mt5", Symbol: "AUDJPYm", Ticket: "tk-" + itoa64(posA), Pnl: -40, Swap: 4.99, Commission: -1},
	})}
	_, _, c2 := s2.floatingCloseNowNet(ctx)
	if c2 {
		t.Error("posB 净估缺 → complete 应 false（available=false 全格「—」）")
	}

	// 锚3：游离腿估不出（D-167 语义随迁——Fresh=false 有价照算；改无价行表达「估不出」）→ complete=false。
	noPrice := &v1.BrokerPosition{
		VenueKey: "binance:spot", Symbol: "SOLUSDT", Ticket: "sol-t", Side: "BUY",
		OpenPrice: 100, CurrentPrice: 0, Volume: 2,
		OpenedAt: timestamppb.New(time.Now().Add(-time.Hour)),
	}
	_, _, c3 := newSvc(noPrice).floatingCloseNowNet(ctx)
	if c3 {
		t.Error("游离腿 net 估缺（现价=0）→ complete 应 false")
	}

	// 锚4：全 NULL→available=false 端端到端（GetPnlAnchor 下发语义）。
	resp, err := s2.GetPnlAnchor(ctx, nil)
	if err != nil {
		t.Fatalf("GetPnlAnchor: %v", err)
	}
	if resp.Msg.FloatingAvailable {
		t.Error("仓净估缺 → FloatingAvailable 应 false")
	}
}

func itoa64(v int64) string { return strconv.FormatInt(v, 10) }
