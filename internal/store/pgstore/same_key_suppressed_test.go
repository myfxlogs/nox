// d592_alert_dedup_test.go D-592 S1：insertAlert 同键窗口抑制六锚（A1-A6）。
// 去重态=DB 实查非进程内存（KB cron-monitor-state-persistence——xasset 5min 批处理
// 进程态活不过一轮）；fail-open=查询/归一化出错放行原路径（止血票不引新静默风险）。
package pgstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/platform/alerttext"
	"arb/internal/platform/testutil"
)

func d592Store(t *testing.T) *Store {
	t.Helper()
	st, err := New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := st.Pool().Exec(context.Background(), `TRUNCATE alerts`); err != nil {
		t.Fatalf("truncate alerts: %v", err)
	}
	return st
}

func d592Count(t *testing.T, st *Store) int {
	t.Helper()
	var n int
	if err := st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM alerts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A1：同键窗内第二条被抑制（1 行；alertHook 仅 1 次调用=feishu/topic 通道同抑）。
func TestD592_SameKeySuppressed(t *testing.T) {
	st := d592Store(t)
	var hooks int32 = 0
	st.SetAlertHook(func(a Alert) { hooks++ })
	if err := st.InsertAlert(context.Background(), nil, "warn", "cex-proxy 通道连接失败（连续 3 次）"); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertAlert(context.Background(), nil, "warn", "cex-proxy 通道连接失败（连续 5 次）"); err != nil {
		t.Fatal(err) // 数字段归一后同键——不报错但也不落行
	}
	if n := d592Count(t, st); n != 1 {
		t.Fatalf("同键窗内 want 1 行, got %d", n)
	}
	if hooks != 1 {
		t.Fatalf("hook（topic/feishu 通道）want 1 次调用, got %d", hooks)
	}
}

// A2：异键两条全放（不同 venue/符号文案归一后仍异键）。
func TestD592_DifferentKeyBothInsert(t *testing.T) {
	st := d592Store(t)
	if err := st.InsertAlert(context.Background(), nil, "warn", "fetch binance PAXGUSDT: premiumIndex 418"); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertAlert(context.Background(), nil, "warn", "fetch binance XAUTUSDT: premiumIndex 418"); err != nil {
		t.Fatal(err)
	}
	if n := d592Count(t, st); n != 2 {
		t.Fatalf("异键 want 2 行, got %d", n)
	}
}

// A3：窗过期后同键重发（窗口注入 50ms，过窗后第二条照落）。
func TestD592_WindowExpiryResend(t *testing.T) {
	st := d592Store(t)
	st.SetAlertDedupWindow(50 * time.Millisecond)
	if err := st.InsertAlert(context.Background(), nil, "warn", "mt5 停更"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	if err := st.InsertAlert(context.Background(), nil, "warn", "mt5 停更"); err != nil {
		t.Fatal(err)
	}
	if n := d592Count(t, st); n != 2 {
		t.Fatalf("窗过期同键 want 2 行, got %d", n)
	}
}

// A4：恢复类直通——故障/恢复是不同 key，各自首条都落（flap 交替每侧一条/窗）。
func TestD592_RecoveryDistinctKey(t *testing.T) {
	st := d592Store(t)
	if err := st.InsertAlert(context.Background(), nil, "warn", "cex-proxy 通道连接失败（连续 3 次）"); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertAlert(context.Background(), nil, "warn", "cex-proxy 通道已恢复"); err != nil {
		t.Fatal(err)
	}
	if n := d592Count(t, st); n != 2 {
		t.Fatalf("恢复类与故障类异键 want 2 行, got %d", n)
	}
}

// A5：fail-open——dedup 查询出错（pool 已关）不阻断原路径：错误=插入层错误
// （流程到达 INSERT=去重错误被吞放行），禁静默升级为新丢告警面。
func TestD592_FailOpenOnError(t *testing.T) {
	st := d592Store(t)
	st.SetAlertDedupWindow(30 * time.Minute)
	st.Close() // 使 dedup 查询与插入同败——断言错误形态出自插入层
	err := st.InsertAlert(context.Background(), nil, "warn", "cex-proxy 通道连接失败（连续 3 次）")
	if err == nil {
		t.Fatal("pool 已关，插入应报错")
	}
	if !strings.Contains(err.Error(), "insert alert") {
		t.Fatalf("fail-open 应到达插入路径（err=insert 层）, got: %v", err)
	}
}

// A6：数字归一化命中——age=1m0s 与 age=2m0s 同键（ integration）；级别并入键。
func TestD592_NormalizationAndLevel(t *testing.T) {
	st := d592Store(t)
	if err := st.InsertAlert(context.Background(), nil, "warn", "xasset ticks stale age=1m0s > 30s"); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertAlert(context.Background(), nil, "warn", "xasset ticks stale age=2m0s > 30s"); err != nil {
		t.Fatal(err)
	}
	if n := d592Count(t, st); n != 1 {
		t.Fatalf("数字归一化后同键 want 1 行, got %d", n)
	}
	// level 并入键：同 message 异 level 不互抑
	if err := st.InsertAlert(context.Background(), nil, "critical", "xasset ticks stale age=1m0s > 30s"); err != nil {
		t.Fatal(err)
	}
	if n := d592Count(t, st); n != 2 {
		t.Fatalf("level 并键后异级 want 2 行, got %d", n)
	}
	k1 := alerttext.AlertDedupKey("warn", "age=1m0s")
	if k1 != alerttext.AlertDedupKey("warn", "age=2m0s") {
		t.Fatal("归一化：age 数字段应同键")
	}
	if k1 == alerttext.AlertDedupKey("critical", "age=1m0s") {
		t.Fatal("level 应并入键")
	}
}

// A7（D-592 R1）：同键 critical 窗内第二条照常落库（资金路径事件审计不丢），
// 仅跳 alertHook（topic/feishu 推送仍抑）；warn 回归见 A1（0 行 0 推）。
func TestD592R1_CriticalDupInsertsSkipsPush(t *testing.T) {
	st := d592Store(t)
	var hooks int32 = 0
	st.SetAlertHook(func(a Alert) { hooks++ })
	if err := st.InsertAlert(context.Background(), nil, "critical", "残腿平仓失败 position=3"); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertAlert(context.Background(), nil, "critical", "残腿平仓失败 position=7"); err != nil {
		t.Fatal(err) // 数字归一同键——critical 须仍落行（审计），只跳推送
	}
	if n := d592Count(t, st); n != 2 {
		t.Fatalf("critical 同键窗内 want 2 行（审计不丢）, got %d", n)
	}
	if hooks != 1 {
		t.Fatalf("hook（topic/feishu 通道）want 1 次调用, got %d", hooks)
	}
}
