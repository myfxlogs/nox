// d007_gatewatch_test.go —— D-007 S1 验收锚：闸态 diff 器。
// A1 报价到→元组翻转→pending_entries emit（冷窗病灶闭环）；
// A2 同值帧→元组不变→零 emit（翻转驱动非逐 tick）；
// A3 boot 基线不推（首拍只建档）。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
)

// d007WatchSvc 复用 d580 全闸夹具 + refreshTopic 接线 + 缩短合批窗。
func d007WatchSvc(t *testing.T) (*Service, <-chan string, context.CancelFunc) {
	t.Helper()
	svc := d580ReadySvc(t)
	svc.SetRefreshTopic(bus.NewTopic[string]())
	w := svc.pgWatch()
	w.flushEvery = 20 * time.Millisecond
	w.staleScanEvery = time.Hour // 测试内不看门狗干扰
	ch, _ := svc.refreshTopic.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	go svc.RunPendingGateWatch(ctx)
	t.Cleanup(cancel)
	return svc, ch, cancel
}

func d007Drain(ch <-chan string) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// d007WaitBaseline 等 boot 基线拍落定（canonical 索引建档后才收帧——早于索引的帧按
// 设计被索引过滤丢弃，与生产 boot 首拍空窗同语义）。
func d007WaitBaseline(t *testing.T, svc *Service) {
	t.Helper()
	w := svc.pgWatch()
	deadline := time.Now().Add(2 * time.Second)
	for {
		w.mu.Lock()
		_, seeded := w.last[905]
		primed := w.primed
		w.mu.Unlock()
		if seeded && primed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("boot baseline not established")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A1+A3：boot 建档不推；报价帧 → 元组翻（实况不可得→价差整串）→ emit。
func TestD007_GateWatch_EmitOnFlip(t *testing.T) {
	svc, ch, _ := d007WatchSvc(t)
	// boot 基线建档（primed 首拍吞掉不推——此处应无 emit）。
	d007WaitBaseline(t, svc)
	select {
	case tpc := <-ch:
		t.Fatalf("boot baseline must not emit, got %q", tpc)
	case <-time.After(100 * time.Millisecond):
	}
	// 报价到 → waitReason 从「实况价差不可得」翻成价差整串 → emit。
	d580Publish(svc, 2000, 2002.06, 0)
	select {
	case tpc := <-ch:
		if tpc != RefreshPendingEntries {
			t.Fatalf("topic=%q want pending_entries", tpc)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("quote flip did not emit pending_entries")
	}
}

// A2：同值帧（元组不变）→ 零 emit。
func TestD007_GateWatch_NoEmitOnSameTuple(t *testing.T) {
	svc, ch, _ := d007WatchSvc(t)
	d007WaitBaseline(t, svc)
	d580Publish(svc, 2000, 2002.06, 0)
	select {
	case <-ch: // 吃掉首次翻转 emit
	case <-time.After(2 * time.Second):
		t.Fatal("initial flip emit missing")
	}
	d007Drain(ch)
	// 同值再发 + 无关 canonical 帧 → 元组不变 → 窗内零 emit。
	d580Publish(svc, 2000, 2002.06, 0)
	svc.qbus.Publish(bus.Quote{Symbol: "UNRELATED", Venue: "x", VenueType: "perp",
		Bid: 1, Ask: 1, Time: time.Now()})
	select {
	case tpc := <-ch:
		t.Fatalf("same-tuple frames must not emit, got %q", tpc)
	case <-time.After(300 * time.Millisecond):
	}
}

// A4：配置写（allow_rules topic）→ 全量重估；组合退出 pending（持仓）→ 成员变化 emit。
// 覆盖「闸态变化的第二输入域=组合/规则写」的失效触发路径。
func TestD007_GateWatch_AllowRulesTrigger(t *testing.T) {
	svc, ch, _ := d007WatchSvc(t)
	d007WaitBaseline(t, svc)
	d580Publish(svc, 2000, 2002.06, 0)
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("initial flip emit missing")
	}
	d007Drain(ch)
	// 撤 venue 放行 → 元组翻（闸1 拦截）→ emit（先写库再发 topic，镜像生产写源序）。
	if _, err := svc.store.Pool().Exec(context.Background(),
		`UPDATE autotrade_allow_rules SET enabled=false WHERE value='3001157:mt5'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = svc.store.Pool().Exec(context.Background(),
			`UPDATE autotrade_allow_rules SET enabled=true WHERE value='3001157:mt5'`)
	})
	svc.PublishDataRefresh(RefreshAllowRules)
	deadline := time.After(2 * time.Second)
	for { // 本测试 ch 同收 allow_rules 本体——读到 pending_entries 为止。
		select {
		case tpc := <-ch:
			if tpc == RefreshPendingEntries {
				return
			}
		case <-deadline:
			t.Fatal("allow_rules write did not trigger re-eval emit")
		}
	}
}

// S2 锚：闸6 typed 键直查——3001157:mt5 cap=50 < 实占 0+proposed 100 → 拒（D-009 S4
// 实占口径：在途系统仓；typed 键剥 bare 恒 miss=永不拦的死键回归锚不变）。
func TestD007_VenueCapGateTypedKey(t *testing.T) {
	svc := d580ReadySvc(t)
	if _, err := svc.store.Pool().Exec(context.Background(),
		`UPDATE fin_venue_exposure_caps SET cap_usd=50 WHERE venue_key='3001157:mt5'`); err != nil {
		t.Fatal(err)
	}
	row := d580Row(t, svc)
	if row.Ready || !strings.Contains(row.WaitReason, "敞口超限") {
		t.Fatalf("cap=50 应拦（typed 键命中）: ready=%v reason=%q", row.Ready, row.WaitReason)
	}
}
