package dashboard

// D-571 包C 锚：T1 case4 回放帧=DB 行转事件逐条；T2 posChange kick→diff 仅变行发；
// T3 hedge kick→case17 载荷帧。DSN 真库（positions 行 CreatePosition 落库→OnPositionChange 同源触发）。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"arb/internal/bus"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"
	"arb/internal/testutil/storetest"
)

// d571cFixture DSN 真库 svc+posMon+四域 topic（bpm/acctMon nil=enrich 诚实缺省）。
type d571cFixture struct {
	st   *pgstore.Store
	svc  *Service
	mon  *PositionMonitor
	pos  *bus.Topic[*v1.Position]
	hedg *bus.Topic[*v1.HedgeData]
	heal *bus.Topic[*v1.SourceHealth]
}

func d571cSetup(t *testing.T) *d571cFixture {
	t.Helper()
	st := storetest.SharedStore(t, "ARB_TEST_PG_DSN")
	svc := &Service{store: st, qbus: bus.New()}
	f := &d571cFixture{
		st:   st,
		svc:  svc,
		pos:  bus.NewTopic[*v1.Position](),
		hedg: bus.NewTopic[*v1.HedgeData](),
		heal: bus.NewTopic[*v1.SourceHealth](),
	}
	f.mon = NewPositionMonitor(svc, f.pos, f.hedg, f.heal)
	// 防抖窗收缩（测试节奏）；defer 由测试持有还原。
	old := posFlushDelay
	posFlushDelay = 10 * time.Millisecond
	t.Cleanup(func() { posFlushDelay = old })
	return f
}

func drainPosEvents(ch <-chan *v1.Position) []*v1.Position {
	var out []*v1.Position
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// T1：case4 回放帧=DB 行转事件逐条——boot 组装 latest（不广播）→PositionEvents 逐行。
func TestD571C_T1_PositionReplay(t *testing.T) {
	f := d571cSetup(t)
	ctx := context.Background()
	id, err := f.st.CreatePosition(ctx, &pgstore.Position{Type: "CROSS_VENUE", Status: "OPEN", ExpectedNetBps: 12.5})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() {
		_, _ = f.st.Pool().Exec(ctx, "DELETE FROM positions WHERE id=$1", id)
	}()
	evs := f.mon.PositionEvents(ctx) // 触发 boot+回放
	var hit *v1.Position
	for _, ev := range evs {
		if ev.Id == id {
			hit = ev
		}
	}
	if hit == nil {
		t.Fatalf("回放帧缺 pos=%d（共 %d 帧）", id, len(evs))
	}
	if hit.Status != "OPEN" || hit.Type != "CROSS_VENUE" || hit.ExpectedNetBps != 12.5 {
		t.Fatalf("回放行字段不符: %+v", hit)
	}
}

// T2：posChange kick→重组装 proto.Equal diff→仅变行发 case4；无变 kick 零事件。
func TestD571C_T2_KickDiffOnlyChanged(t *testing.T) {
	f := d571cSetup(t)
	ctx := context.Background()
	posChange := bus.NewTopic[struct{}]()
	f.st.OnPositionChange = func() { posChange.Publish(struct{}{}) }
	defer func() { f.st.OnPositionChange = nil }()
	// 订阅→kick 转发（生产=main:posMonC.posChange goroutine）。
	go func() {
		ch, _ := posChange.Subscribe()
		for range ch {
			f.mon.Kick()
		}
	}()
	evCh, _ := f.pos.Subscribe()
	f.mon.PositionEvents(ctx) // boot 填 latest（先于订阅事件，不广播）
	drainPosEvents(evCh)      // 清残

	id, err := f.st.CreatePosition(ctx, &pgstore.Position{Type: "CROSS_VENUE", Status: "OPEN", ExpectedNetBps: 3})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() {
		_, _ = f.st.Pool().Exec(ctx, "DELETE FROM positions WHERE id=$1", id)
	}()
	// CreatePosition 已发 posChange——等防抖尾沿后应有且仅有该行事件。
	time.Sleep(80 * time.Millisecond)
	got := drainPosEvents(evCh)
	if len(got) != 1 || got[0].Id != id || got[0].Status != "OPEN" {
		t.Fatalf("create kick 事件期望 1 帧 pos=%d OPEN, got %+v", id, got)
	}
	// 状态迁移→kick→diff 仅该行状态变更帧。
	if err := f.st.UpdatePositionStatus(ctx, id, "CLOSING"); err != nil {
		t.Fatalf("update: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	got = drainPosEvents(evCh)
	if len(got) != 1 || got[0].Id != id || got[0].Status != "CLOSING" {
		t.Fatalf("update kick 事件期望 1 帧 CLOSING, got %+v", got)
	}
	// 零变化 kick→diff 空→零事件（diff 兜底防噪声帧）。
	f.mon.Kick()
	time.Sleep(80 * time.Millisecond)
	if got := drainPosEvents(evCh); len(got) != 0 {
		t.Fatalf("零变 kick 应零事件, got %+v", got)
	}
}

// T3：hedge kick→同组装路径重组装→proto.Equal diff→case17 HedgeData 单槽载荷帧。
// 语义锚：boot 已采 latest——数据变更后 kick 才出帧（diff 兜底防同值噪声）。
func TestD571C_T3_HedgeKickPayload(t *testing.T) {
	f := d571cSetup(t)
	ctx := context.Background()
	evCh, _ := f.hedg.Subscribe()
	boot := f.mon.HedgeEvent(ctx) // boot（不广播）
	if boot == nil {
		t.Skip("test 库无 hedge 数据——回放帧空，跳过载荷断言")
	}
	time.Sleep(30 * time.Millisecond)
	select {
	case ev := <-evCh:
		t.Fatalf("boot 不应广播, got %+v", ev)
	default:
	}
	// 数据变更（新流水行）→kick→diff 出新 HedgeData。
	txID := fmt.Sprintf("d571c_t3_%d", time.Now().UnixNano())
	if _, err := f.st.Pool().Exec(ctx,
		`INSERT INTO fin_trx_txs (tx_id, ts, kind, amount_sun, counterparty, cheat_status, fetched_at)
		 VALUES ($1, now(), 'reward', 1, 'd571c', false, now()) ON CONFLICT (tx_id) DO NOTHING`, txID); err != nil {
		t.Fatalf("insert trx tx: %v", err)
	}
	defer func() {
		_, _ = f.st.Pool().Exec(ctx, "DELETE FROM fin_trx_txs WHERE tx_id=$1", txID)
	}()
	f.mon.KickHedge()
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case ev := <-evCh:
			if ev == nil {
				t.Fatal("nil hedge frame")
			}
			// 载荷帧应含新流水行（组装与 ListHedgeFlow 同路径）。
			for _, it := range ev.Flow {
				if it.TxId == txID {
					return
				}
			}
			t.Fatalf("case17 帧到达但无新流水行 %s（flow=%d 条）", txID, len(ev.Flow))
		case <-time.After(100 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("hedge kick 后 2s 无 case17 帧")
			}
		}
	}
}

// T4 R2 F-1：health 域「kick→真发射」——venue_online 读源翻转+OnHealthDirty
// 脏化→重组装 diff→case5 行事件（不修则 venue_online 翻转死踢零帧）。
func TestD571C_R2_T4_HealthKickEmits(t *testing.T) {
	f := d571cSetup(t)
	ctx := context.Background()
	online := map[string]bool{"t4venue:mt5": true}
	f.svc.SetStreamOnlineSource(func() map[string]bool { return online })
	evCh, _ := f.heal.Subscribe()
	boot := f.mon.HealthEvents(ctx) // boot 采 latest（含 venue_online:true 行）不广播
	var bootHit *v1.SourceHealth
	for _, h := range boot {
		if h.Source == "venue_online:t4venue:mt5" {
			bootHit = h
		}
	}
	if bootHit == nil || bootHit.State != "online" {
		t.Fatalf("boot 回放应含 venue_online 行: %+v", bootHit)
	}
	time.Sleep(30 * time.Millisecond)
	if n := len(evCh); n > 0 {
		t.Fatalf("boot 不应广播, got %d", n)
	}
	online["t4venue:mt5"] = false // 流状态翻转（生产=StreamStateSink 事件）
	f.mon.OnHealthDirty()
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case ev := <-evCh:
			if ev.Source == "venue_online:t4venue:mt5" {
				if ev.State != "offline" {
					t.Fatalf("翻转后应 offline: %+v", ev)
				}
				return
			}
		case <-time.After(50 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("health kick 后 2s 无 case5 帧")
			}
		}
	}
}
