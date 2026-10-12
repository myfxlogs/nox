// d083_session_blocked_test.go —— D-083 锚(a)：trade_session canonical 撞车修复。
// 两 brokerSym 共 canonical（XAUUSDm/XAUUSD247m→XAUUSD）时，blocked 符号只发
// native 行不发 canonical 行（D-383 A2.3 同闸），且每符号带 `#n` 窗计数行。
package adapter

import (
	"strings"
	"testing"
	"time"

	mt5pb "arb/internal/data/adapter/gen/mtapi/mt5"
)

// d083SessionDays 7 日周表夹具：周一全天窗（0-1440）、周二 08:00-10:00（服务器本地）。
func d083SessionDays() []*mt5pb.SessionsForDay {
	days := make([]*mt5pb.SessionsForDay, 7)
	for i := range days {
		days[i] = &mt5pb.SessionsForDay{}
	}
	days[1].Sessions = []*mt5pb.Session{{StartTime: 0, EndTime: 1440}}
	days[2].Sessions = []*mt5pb.Session{{StartTime: 480, EndTime: 600}}
	return days
}

func TestD083_SessionCanonicalEmitBlocked(t *testing.T) {
	srvLoc := time.UTC
	srvNow := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) // 周一（锚点在窗覆盖周内）
	days := d083SessionDays()
	blocked := map[string]bool{"XAUUSD247m": true}

	// blocked 符号（XAUUSD247m→XAUUSD）：canonical 行零，native 行齐（2 窗+#n=2）。
	outB := sessionFactsFor("3001157", "XAUUSD", "XAUUSD247m", days, srvLoc, srvNow, blocked)
	var canonWins, nativeWins int
	nVal, nOK := -1.0, false
	for _, f := range outB {
		switch {
		case strings.HasPrefix(f.Symbol, "XAUUSD#"):
			canonWins++
		case f.Symbol == "XAUUSD247m#n":
			if f.Unit != "utc_session_count" {
				t.Fatalf("#n 行 unit 应 utc_session_count, got %q", f.Unit)
			}
			nVal, nOK = f.Value, true
		case strings.HasPrefix(f.Symbol, "XAUUSD247m#"):
			nativeWins++
		default:
			t.Fatalf("意外行 symbol=%q", f.Symbol)
		}
	}
	if canonWins != 0 {
		t.Fatalf("blocked 符号应零 canonical 行, got %d", canonWins)
	}
	if nativeWins != 2 || !nOK || nVal != 2 {
		t.Fatalf("blocked 符号 native 应 2 窗+#n=2, got wins=%d n=%v ok=%v", nativeWins, nVal, nOK)
	}

	// 未 blocked 符号（XAUUSDm→XAUUSD）：canonical+native 双发，各 2 窗+#n=2。
	outA := sessionFactsFor("3001157", "XAUUSD", "XAUUSDm", days, srvLoc, srvNow, blocked)
	var cWins, nWins int
	cN, nN := -1.0, -1.0
	for _, f := range outA {
		switch {
		case f.Symbol == "XAUUSD#n":
			cN = f.Value
		case f.Symbol == "XAUUSDm#n":
			nN = f.Value
		case strings.HasPrefix(f.Symbol, "XAUUSD#"):
			cWins++
		case strings.HasPrefix(f.Symbol, "XAUUSDm#"):
			nWins++
		}
	}
	if cWins != 2 || cN != 2 || nWins != 2 || nN != 2 {
		t.Fatalf("未 blocked 应 canonical 2+#n=2 / native 2+#n=2, got %v/%v/%v/%v", cWins, cN, nWins, nN)
	}

	// 空 blocked 集=旧行为：两者都发 canonical。
	outNil := sessionFactsFor("3001157", "XAUUSD", "XAUUSD247m", days, srvLoc, srvNow, nil)
	canonNil := 0
	for _, f := range outNil {
		if strings.HasPrefix(f.Symbol, "XAUUSD#") && f.Unit == "utc_session" {
			canonNil++
		}
	}
	if canonNil != 2 {
		t.Fatalf("blocked=nil 应照常发 canonical 2 窗, got %d", canonNil)
	}
}
