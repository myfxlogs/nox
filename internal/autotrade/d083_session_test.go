// d083_session_test.go —— D-083 锚(b)(c)：engine 周表装表 `#n` 幽灵窗截断 + 全天窗判定。
// (b) facts 含 XPTUSD#0..#9 + XPTUSD#n=8 → 装表仅 8 窗（#8/#9 残留丢弃）；
// (c) inSession 全天窗（start=end=0，MT5 end=1440 折零）周一任意分钟=true。
package autotrade

import (
	"fmt"
	"testing"
	"time"

	"arb/internal/fact"
)

// TestD083_SessionTableGhostWindowsDropped (b)：#n 计数行截断越界幽灵窗。
// 无 #n 行=旧数据不过滤（兼容回归同测）。
func TestD083_SessionTableGhostWindowsDropped(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	fs := make([]fact.Fact, 0, 11)
	for i := 0; i < 10; i++ {
		fs = append(fs, fact.Fact{
			Kind: fact.KindTradeSession, Venue: "3001157", Symbol: fmt.Sprintf("XPTUSD#%d", i),
			Value: float64(1*1e8 + 480*1e4 + 600), Unit: "utc_session", Ts: time.Now(), Src: "mt5",
		})
	}
	// 计数行：真实窗数 8 → #8/#9 幽灵残留应被丢弃。
	fs = append(fs, fact.Fact{
		Kind: fact.KindTradeSession, Venue: "3001157", Symbol: "XPTUSD#n",
		Value: 8, Unit: "utc_session_count", Ts: time.Now(), Src: "mt5",
	})
	// 对照符号：无 #n 行 → 全保留（旧数据兼容）。
	fs = append(fs, fact.Fact{
		Kind: fact.KindTradeSession, Venue: "3001157", Symbol: "LEGACY#0",
		Value: float64(2*1e8 + 60*1e4 + 120), Unit: "utc_session", Ts: time.Now(), Src: "mt5",
	})
	if err := s.InsertFacts(ctx029(), fs); err != nil {
		t.Fatalf("seed facts: %v", err)
	}
	e := d014EconEngine(s, d404Quotes(time.Now(), 4000, 4000, 0), true)
	tbl := e.loadSessionTable(ctx029())
	if got := len(tbl["3001157|XPTUSD"]); got != 8 {
		t.Fatalf("#n=8 应截断为 8 窗（#8/#9 幽灵丢弃）, got %d", got)
	}
	if got := len(tbl["3001157|LEGACY"]); got != 1 {
		t.Fatalf("无 #n 行旧数据应不过滤, got %d", got)
	}
	// 计数行本身不入窗（若入窗会解码成 wday=0 start=0 end=8 伪窗，长度断言已兜；再钉值域）。
	for _, w := range tbl["3001157|XPTUSD"] {
		if w.wday != 1 || w.startMin != 480 || w.endMin != 600 {
			t.Fatalf("窗解码异常（疑似 #n 行混入）: %+v", w)
		}
	}
}

// TestD083_InSessionFullDayWindow (c)：start=end=0 全天窗——当天任意分钟在市；
// 普通窗/跨午夜窗回归不变。
func TestD083_InSessionFullDayWindow(t *testing.T) {
	full := sessionWindow{wday: 1, startMin: 0, endMin: 0} // 周一 00:00-24:00（247m 形态）
	mon := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)    // 周一
	if !full.inSession(mon.Add(13*time.Hour + 37*time.Minute)) {
		t.Fatal("全天窗周一 13:37 应在市")
	}
	if !full.inSession(mon.Add(23*time.Hour + 59*time.Minute)) {
		t.Fatal("全天窗周一 23:59 应在市")
	}
	if full.inSession(mon.AddDate(0, 0, 1)) {
		t.Fatal("全天窗周二 00:00 不应在市")
	}
	// 普通窗回归：周五 08:00-20:00。
	n := sessionWindow{wday: 5, startMin: 480, endMin: 1200}
	if !n.inSession(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("普通窗周五 12:00 应在市")
	}
	if n.inSession(time.Date(2026, 10, 2, 21, 0, 0, 0, time.UTC)) {
		t.Fatal("普通窗周五 21:00 不应在市")
	}
	// 跨午夜窗回归：周日 07:00→周一 05:00。
	wrap := sessionWindow{wday: 0, startMin: 420, endMin: 300}
	if !wrap.inSession(time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)) {
		t.Fatal("跨午夜窗周日 08:00（尾段）应在市")
	}
	if !wrap.inSession(time.Date(2026, 10, 5, 4, 0, 0, 0, time.UTC)) {
		t.Fatal("跨午夜窗周一 04:00（次日头段）应在市")
	}
	if wrap.inSession(time.Date(2026, 10, 5, 6, 0, 0, 0, time.UTC)) {
		t.Fatal("跨午夜窗周一 06:00（超 end）不应在市")
	}
}
