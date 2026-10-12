package dephealth

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// d038FakeAfter 注入伪 afterFunc——回调收集进 cbs 由测试按伪时钟手动触发；
// 返回的占位 Timer 仅满足 Stop() 调用面。
func d038FakeAfter(cbs *[]func()) func() {
	old := afterFunc
	afterFunc = func(d time.Duration, f func()) *time.Timer {
		*cbs = append(*cbs, f)
		return time.NewTimer(time.Hour)
	}
	return func() { afterFunc = old }
}

// TestD579_T1_StateMachine 状态机全迁移动线（clock 注入控时）：
// 连败2不警→连败3 degraded 进 30s 确认窗（仍不警）→确认窗到点 warn→
// degraded 续败不重警→首成功 recovered 警含时长→healthy 后再败回 suspect。
func TestD579_T1_StateMachine(t *testing.T) {
	resetForTest()
	cur := time.Unix(1000, 0)
	oldNow := nowFn
	nowFn = func() time.Time { return cur }
	var cbs []func()
	restoreAfter := d038FakeAfter(&cbs)
	defer func() { nowFn = oldNow; restoreAfter(); resetForTest() }()

	var muA sync.Mutex
	var alerts []string
	Init(func(level, msg string) { muA.Lock(); defer muA.Unlock(); alerts = append(alerts, level+"|"+msg) })
	initAt = cur.Add(-BootSilenceWindow) // 本锚测确认窗语义——静默窗置已过期
	gotAlerts := func() []string { muA.Lock(); defer muA.Unlock(); return append([]string{}, alerts...) }

	key := "gate:perp:rest"
	Mark(key, false, errors.New("e1"))
	Mark(key, false, errors.New("e2"))
	if n := len(gotAlerts()); n != 0 {
		t.Fatalf("连败2 不应告警, got %d: %v", n, alerts)
	}
	Mark(key, false, errors.New("e3"))
	if n := len(gotAlerts()); n != 0 {
		t.Fatalf("连败3 进确认窗仍不警（D-038①）, got %d: %v", n, alerts)
	}
	if len(cbs) != 1 {
		t.Fatalf("进 degraded 应挂确认定时器, cbs=%d", len(cbs))
	}
	cur = cur.Add(10 * time.Second)
	Mark(key, false, errors.New("e4"))
	if n := len(gotAlerts()); n != 0 {
		t.Fatalf("确认窗内续败不警, got %d", n)
	}
	rows := Snapshot()
	if len(rows) != 1 || rows[0].State != StateDegraded || rows[0].ConsecFails != 4 {
		t.Fatalf("snapshot 应 degraded×4: %+v", rows)
	}
	cur = cur.Add(20 * time.Second) // 距 degraded 30s——确认窗到点
	cbs[0]()                        // 定时器回调触发 → warn
	if n := len(gotAlerts()); n != 1 || !strings.HasPrefix(alerts[0], "warn|") || !strings.Contains(alerts[0], "连败 4") {
		t.Fatalf("确认窗满应 warn 一次: %v", alerts)
	}
	cur = cur.Add(30 * time.Second) // 中断累计 60s
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

// TestD038_ConfirmWindowRecoverSilent ①：3 连败进 degraded + 10s 内成功
// → 静默回 healthy 零告警（确认窗内恢复不发任何告警）。
func TestD038_ConfirmWindowRecoverSilent(t *testing.T) {
	resetForTest()
	cur := time.Unix(3000, 0)
	oldNow := nowFn
	nowFn = func() time.Time { return cur }
	var cbs []func()
	restoreAfter := d038FakeAfter(&cbs)
	defer func() { nowFn = oldNow; restoreAfter(); resetForTest() }()

	var muA sync.Mutex
	var alerts []string
	Init(func(level, msg string) { muA.Lock(); defer muA.Unlock(); alerts = append(alerts, level+"|"+msg) })
	initAt = cur.Add(-BootSilenceWindow)

	key := "3001157:mt5:rpc"
	for i := 0; i < 3; i++ {
		Mark(key, false, errors.New("DeadlineExceeded"))
	}
	if st := Snapshot()[0].State; st != StateDegraded {
		t.Fatalf("3 连败应 degraded, got %s", st)
	}
	cur = cur.Add(10 * time.Second) // 确认窗内（10s<30s）
	Mark(key, true, nil)
	if n := len(alerts); n != 0 {
		t.Fatalf("确认窗内恢复应零告警, got %d: %v", n, alerts)
	}
	if st := Snapshot()[0].State; st != StateHealthy {
		t.Fatalf("成功应回 healthy, got %s", st)
	}
}

// TestD038_ConfirmWindowExpiryWarn ①：3 连败 +35s 无成功 → 确认窗满 warn 一次；
// 之后恢复发 info（中断自 downSince 起算）。
func TestD038_ConfirmWindowExpiryWarn(t *testing.T) {
	resetForTest()
	cur := time.Unix(4000, 0)
	oldNow := nowFn
	nowFn = func() time.Time { return cur }
	var cbs []func()
	restoreAfter := d038FakeAfter(&cbs)
	defer func() { nowFn = oldNow; restoreAfter(); resetForTest() }()

	var muA sync.Mutex
	var alerts []string
	Init(func(level, msg string) { muA.Lock(); defer muA.Unlock(); alerts = append(alerts, level+"|"+msg) })
	initAt = cur.Add(-BootSilenceWindow)

	key := "okx:perp:rest"
	for i := 0; i < 3; i++ {
		Mark(key, false, errors.New("timeout"))
	}
	cur = cur.Add(35 * time.Second) // 满 30s 确认窗仍无成功
	cbs[0]()
	if n := len(alerts); n != 1 || !strings.HasPrefix(alerts[0], "warn|") {
		t.Fatalf("确认窗满应一条 warn: %v", alerts)
	}
	cur = cur.Add(10 * time.Second) // 中断累计 45s
	Mark(key, true, nil)
	if n := len(alerts); n != 2 || !strings.HasPrefix(alerts[1], "info|") || !strings.Contains(alerts[1], "中断 45s") {
		t.Fatalf("恢复应 info 含时长: %v", alerts)
	}
}

// TestD038_MethodLevelNotCounted ②：Unimplemented×5 + InvalidArgument×5（grpc status，
// 生产 depUnaryInterceptor 实际形态）不计连败不劣化
// （方法级错=单次调用问题，非面连通性故障）。
func TestD038_MethodLevelNotCounted(t *testing.T) {
	resetForTest()
	cur := time.Unix(5000, 0)
	oldNow := nowFn
	nowFn = func() time.Time { return cur }
	defer func() { nowFn = oldNow; resetForTest() }()

	var muA sync.Mutex
	var alerts []string
	Init(func(level, msg string) { muA.Lock(); defer muA.Unlock(); alerts = append(alerts, level+"|"+msg) })
	initAt = cur.Add(-BootSilenceWindow)

	key := "3001157:mt5:rpc"
	for i := 0; i < 5; i++ {
		Mark(key, false, status.Error(codes.Unimplemented, "not implemented"))
		Mark(key, false, status.Error(codes.InvalidArgument, "bad arg"))
	}
	r := Snapshot()[0]
	if r.ConsecFails != 0 || r.State == StateDegraded {
		t.Fatalf("方法级错不应连败/劣化: %+v", r)
	}
	if n := len(alerts); n != 0 {
		t.Fatalf("方法级错不应告警, got %d", n)
	}
}

// TestD038_BootSilence ③：Init 后 60s 静默窗内只记状态不告警——
// 3 连败 degraded + 确认窗到点重排到窗尾；窗内零告警。
func TestD038_BootSilence(t *testing.T) {
	resetForTest()
	cur := time.Unix(6000, 0)
	oldNow := nowFn
	nowFn = func() time.Time { return cur }
	var cbs []func()
	restoreAfter := d038FakeAfter(&cbs)
	defer func() { nowFn = oldNow; restoreAfter(); resetForTest() }()

	var muA sync.Mutex
	var alerts []string
	Init(func(level, msg string) { muA.Lock(); defer muA.Unlock(); alerts = append(alerts, level+"|"+msg) })
	// initAt=cur（Init 当下）→ 静默窗 [cur, cur+60s)

	key := "3001157:mt5:rpc"
	for i := 0; i < 3; i++ {
		Mark(key, false, errors.New("DeadlineExceeded"))
	}
	if st := Snapshot()[0].State; st != StateDegraded {
		t.Fatalf("静默窗内状态仍应变迁 degraded, got %s", st)
	}
	cur = cur.Add(30 * time.Second) // 确认窗到点但仍在静默窗内（30<60）
	cbs[0]()                        // warnCheck → 静默窗重排到窗尾
	if n := len(alerts); n != 0 {
		t.Fatalf("boot 30s 内应零告警, got %d: %v", n, alerts)
	}
	if len(cbs) != 2 {
		t.Fatalf("静默窗到期应重排一次判定, cbs=%d", len(cbs))
	}
	cur = cur.Add(31 * time.Second) // 出静默窗（61s）
	cbs[1]()                        // 持续劣化过窗仍该警
	if n := len(alerts); n != 1 || !strings.HasPrefix(alerts[0], "warn|") {
		t.Fatalf("持续劣化出静默窗应 warn: %v", alerts)
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
