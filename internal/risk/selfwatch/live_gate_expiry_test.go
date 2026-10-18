package selfwatch

import (
	"context"
	"strings"
	"testing"
	"time"
)

// liveGateStore 覆写 InsertAlertDedup 仿真 DB 同窗去重（同 msg 在窗内只记一次）。
type liveGateStore struct {
	*fakeStore
	seen map[string]time.Time
	now  time.Time
}

func (l *liveGateStore) InsertAlertDedup(_ context.Context, level, msg string, w time.Duration) error {
	key := level + "|" + strings.Split(msg, "（")[0]
	if t0, ok := l.seen[key]; ok && l.now.Sub(t0) < w {
		return nil // 窗内去重（DB InsertAlertDedup 同语义）
	}
	l.seen[key] = l.now
	return l.fakeStore.InsertAlertDedup(context.Background(), level, msg, w)
}

// assertDispatchLastLine D-622 验收锚：LIVE 时效告警文案末行=决策席开工令
// （固定末位=缺席立辨；组装点删转发行本断言必红）。call 形态=level|msg|window。
func assertDispatchLastLine(t *testing.T, call string) {
	t.Helper()
	parts := strings.SplitN(call, "|", 3)
	if len(parts) < 3 {
		t.Fatalf("dedup call 形态异常: %q", call)
	}
	last := parts[1][strings.LastIndexByte(parts[1], '\n')+1:]
	if !strings.Contains(last, "[角色:决策]") || !strings.Contains(last, "读 STATE 本行") {
		t.Fatalf("%s 告警末行应含决策席开工令: %q", parts[0], last)
	}
	if !strings.HasSuffix(parts[1], liveGateRenewDispatch) {
		t.Fatalf("%s 告警末行应与转发表述一致: %q", parts[0], parts[1])
	}
}

// TestCheckLiveGateExpiry D-620 T6：mode=LIVE 剩<24h 两拍一 warn；过期 critical；PAPER/无行静默。
// D-622：warn/critical 文案末行均嵌席位开工令。
func TestCheckLiveGateExpiry(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	st := &liveGateStore{fakeStore: &fakeStore{}, seen: map[string]time.Time{}, now: now}
	soon := now.Add(2 * time.Hour)
	past := now.Add(-time.Hour)
	live := func(m string, exp *time.Time) Deps {
		return Deps{Store: st, Now: func() time.Time { return now },
			LiveGateStatus: func(context.Context) (string, *time.Time, error) { return m, exp, nil }}
	}
	// <24h → warn；第二拍同窗去重仍一 warn
	d := live("LIVE", &soon)
	checkLiveGateExpiry(ctx, d)
	checkLiveGateExpiry(ctx, d)
	if len(st.dedupCalls) != 1 || !strings.HasPrefix(st.dedupCalls[0], "warn|") {
		t.Fatalf("<24h 两拍应落一 warn: %v", st.dedupCalls)
	}
	assertDispatchLastLine(t, st.dedupCalls[0])
	// 过期 → critical
	st.dedupCalls = nil
	checkLiveGateExpiry(ctx, live("LIVE", &past))
	if len(st.dedupCalls) != 1 || !strings.HasPrefix(st.dedupCalls[0], "critical|") {
		t.Fatalf("过期应落 critical: %v", st.dedupCalls)
	}
	assertDispatchLastLine(t, st.dedupCalls[0])
	// PAPER / nil 行 → 静默
	st.dedupCalls = nil
	checkLiveGateExpiry(ctx, live("PAPER", &soon))
	checkLiveGateExpiry(ctx, live("LIVE", nil))
	if len(st.dedupCalls) != 0 {
		t.Fatalf("非 LIVE/无行应静默: %v", st.dedupCalls)
	}
}
