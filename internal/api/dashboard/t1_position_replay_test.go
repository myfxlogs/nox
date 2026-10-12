package dashboard

// D-571 包C 锚：T1 case4 回放帧=DB 行转事件逐条；T2 posChange kick→diff 仅变行发。
// （T3 hedge case17 随族件剔除拆除）。DSN 真库（positions 行 CreatePosition 落库→OnPositionChange 同源触发）。

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/platform/testutil/storetest"
	"nox/internal/store/pgstore"
)

// d571cFixture DSN 真库 svc+posMon+四域 topic（bpm/acctMon nil=enrich 诚实缺省）。
type d571cFixture struct {
	st   *pgstore.Store
	svc  *Service
	mon  *PositionMonitor
	pos  *bus.Topic[*v1.Position]
	heal *bus.Topic[*v1.SourceHealth]
}

func d571cSetup(t *testing.T) *d571cFixture {
	t.Helper()
	st := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	svc := &Service{store: st, qbus: bus.New()}
	f := &d571cFixture{
		st:   st,
		svc:  svc,
		pos:  bus.NewTopic[*v1.Position](),
		heal: bus.NewTopic[*v1.SourceHealth](),
	}
	f.mon = NewPositionMonitor(svc, f.pos, f.heal)
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
