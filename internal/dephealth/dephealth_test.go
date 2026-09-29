package dephealth

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestD579_T1_StateMachine 状态机全迁移动线（clock 注入控时）：
// 连败2不警→连败3 degraded 警→再败不重警→首成功 recovered 警含时长→healthy 后再败回 suspect。
func TestD579_T1_StateMachine(t *testing.T) {
	resetForTest()
	cur := time.Unix(1000, 0)
	oldNow := nowFn
	nowFn = func() time.Time { return cur }
	defer func() { nowFn = oldNow; resetForTest() }()

	var muA sync.Mutex
	var alerts []string
	Init(func(level, msg string) { muA.Lock(); defer muA.Unlock(); alerts = append(alerts, level+"|"+msg) })
	gotAlerts := func() []string { muA.Lock(); defer muA.Unlock(); return append([]string{}, alerts...) }

	key := "gate:perp:rest"
	Mark(key, false, errors.New("e1"))
	Mark(key, false, errors.New("e2"))
	if n := len(gotAlerts()); n != 0 {
		t.Fatalf("连败2 不应告警, got %d: %v", n, alerts)
	}
	Mark(key, false, errors.New("e3"))
	if n := len(gotAlerts()); n != 1 || !strings.HasPrefix(alerts[0], "warn|") || !strings.Contains(alerts[0], "连败 3") {
		t.Fatalf("连败3 应 warn 一次: %v", alerts)
	}
	cur = cur.Add(10 * time.Second)
	Mark(key, false, errors.New("e4"))
	if n := len(gotAlerts()); n != 1 {
		t.Fatalf("degraded 态内继续败不应重警, got %d", n)
	}
	rows := Snapshot()
	if len(rows) != 1 || rows[0].State != StateDegraded || rows[0].ConsecFails != 4 {
		t.Fatalf("snapshot 应 degraded×4: %+v", rows)
	}
	cur = cur.Add(50 * time.Second) // 中断累计 60s
	Mark(key, true, nil)
	if n := len(gotAlerts()); n != 2 || !strings.HasPrefix(alerts[1], "info|") || !strings.Contains(alerts[1], "中断 1m0s") {
		t.Fatalf("恢复应 info 含时长: %v", alerts)
	}
	if Snapshot()[0].State != StateHealthy || Snapshot()[0].ConsecFails != 0 {
		t.Fatalf("恢复后应 healthy 计数归位: %+v", Snapshot()[0])
	}
	Mark(key, false, errors.New("e5"))
	if st := Snapshot()[0].State; st != StateSuspect {
		t.Fatalf("healthy 后败应回 suspect, got %s", st)
	}
	if n := len(gotAlerts()); n != 2 {
		t.Fatalf("suspect 不告警, got %d", n)
	}
}

// TestD579_T1b_IdleWindow 低频面 >30min 无事件透出 idle（degraded 粘滞不降级 idle）。
func TestD579_T1b_IdleWindow(t *testing.T) {
	resetForTest()
	cur := time.Unix(2000, 0)
	oldNow := nowFn
	nowFn = func() time.Time { return cur }
	defer func() { nowFn = oldNow; resetForTest() }()

	Mark("a:rpc", true, nil)
	cur = cur.Add(31 * time.Minute)
	if st := Snapshot()[0].State; st != StateIdle {
		t.Fatalf("31min 无事件应 idle, got %s", st)
	}
	// degraded 粘滞：劣化态静默超窗仍透 degraded（中断未恢复=真相）。
	resetForTest()
	for i := 0; i < 3; i++ {
		Mark("b:rest", false, errors.New("x"))
	}
	cur = cur.Add(40 * time.Minute)
	if st := Snapshot()[0].State; st != StateDegraded {
		t.Fatalf("degraded 静默超窗仍应 degraded, got %s", st)
	}
}

// TestD579_T1c_EmptyKey NilKey 不记账（向后兼容未挂点路径）。
func TestD579_T1c_EmptyKey(t *testing.T) {
	resetForTest()
	defer resetForTest()
	Mark("", false, errors.New("x"))
	if n := len(Snapshot()); n != 0 {
		t.Fatalf("空 key 不应记账, got %d", n)
	}
}

// TestD579_T6_OnChangeKick R3 F-3a：状态变迁才扇出 OnChange（锁外调、天然去重）。
// unknown→healthy 踢、连败 degraded→healthy 踢、healthy→healthy 不踢、degraded 内续败不踢。
func TestD579_T6_OnChangeKick(t *testing.T) {
	resetForTest()
	defer resetForTest()
	var kicks int
	OnChange = func() { kicks++ }

	Mark("k:ws", true, nil) // unknown→healthy
	if kicks != 1 {
		t.Fatalf("unknown→healthy 应踢 1, got %d", kicks)
	}
	Mark("k:ws", true, nil) // healthy→healthy 零踢
	if kicks != 1 {
		t.Fatalf("healthy→healthy 不应踢, got %d", kicks)
	}
	for i := 0; i < 3; i++ {
		Mark("k:ws", false, errors.New("x")) // healthy→suspect(踢1)→suspect(不踢)→degraded(踢1)
	}
	if kicks != 3 {
		t.Fatalf("healthy→suspect→degraded 应再踢 2, got %d", kicks)
	}
	Mark("k:ws", false, errors.New("x")) // degraded 内续败不踢
	if kicks != 3 {
		t.Fatalf("degraded 续败不应踢, got %d", kicks)
	}
	Mark("k:ws", true, nil) // degraded→healthy 踢
	if kicks != 4 {
		t.Fatalf("degraded→healthy 应踢, got %d", kicks)
	}
}
