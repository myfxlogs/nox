package sleepmon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

type fakeStore struct {
	pools   []pgstore.ExplorerIdlePool
	streaks map[string]pgstore.IdleStreak
	alerts  []string
}

func (f *fakeStore) ExplorerIdleFunds(context.Context, time.Time) ([]pgstore.ExplorerIdlePool, error) {
	return f.pools, nil
}
func (f *fakeStore) GetStreaks(context.Context) (map[string]pgstore.IdleStreak, error) {
	out := map[string]pgstore.IdleStreak{}
	for k, v := range f.streaks {
		out[k] = v
	}
	return out, nil
}
func (f *fakeStore) UpsertStreak(_ context.Context, venue, vt string, days int, day time.Time, alerted bool) error {
	f.streaks[venue+"\x00"+vt] = pgstore.IdleStreak{Venue: venue, VenueType: vt, Days: days, CheckDay: day, Alerted: alerted}
	return nil
}
func (f *fakeStore) InsertAlert(_ context.Context, _ *int64, level, msg string) error {
	f.alerts = append(f.alerts, level+":"+msg)
	return nil
}

func belowPool() []pgstore.ExplorerIdlePool {
	return []pgstore.ExplorerIdlePool{{Venue: "binance", VenueType: "spot", Balance: 19300, Currency: "USD", BufferMultiple: 0.5, BelowFloor: true}}
}

// D-534 锚：连续 7 天 below→1 次告警+1 提案行；第 8 天不重复；同日重跑幂等；
// 回上归零再破重新计。
func TestSleepmonStreak(t *testing.T) {
	dir := t.TempDir()
	f := &fakeStore{pools: belowPool(), streaks: map[string]pgstore.IdleStreak{}}
	deps := Deps{Store: f, InboxPath: filepath.Join(dir, "inbox.md")}
	run := func(day string) {
		if err := checkDay(context.Background(), deps, day, bjLoc); err != nil {
			t.Fatal(err)
		}
	}
	for d := 1; d <= 6; d++ {
		run("2026-09-2" + string(rune('0'+d)))
	}
	if len(f.alerts) != 0 {
		t.Fatalf("6 天不应告警: %v", f.alerts)
	}
	run("2026-09-27") // 第 7 天→告警+提案行
	if len(f.alerts) != 1 || !strings.Contains(f.alerts[0], "连续 7 天") {
		t.Fatalf("第 7 天应 1 次告警: %v", f.alerts)
	}
	data, _ := os.ReadFile(deps.InboxPath)
	if strings.Count(string(data), "sleep_money") != 1 || !strings.Contains(string(data), "pending") {
		t.Fatalf("应 1 行 pending 提案: %s", data)
	}
	run("2026-09-28") // 第 8 天不重复告警
	if len(f.alerts) != 1 {
		t.Fatalf("第 8 天不应重复: %v", f.alerts)
	}
	run("2026-09-28") // 同日重跑幂等（days 不重复累加）
	if f.streaks["binance\x00spot"].Days != 8 || len(f.alerts) != 1 {
		t.Fatalf("同日重跑应幂等: %+v", f.streaks["binance\x00spot"])
	}
	// 回上→归零复位
	f.pools[0].BelowFloor = false
	run("2026-09-29")
	if st := f.streaks["binance\x00spot"]; st.Days != 0 || st.Alerted {
		t.Fatalf("回上应归零复位: %+v", st)
	}
	// 再破→重新计，第 7 天才告警
	f.pools[0].BelowFloor = true
	for d := 1; d <= 6; d++ {
		run("2026-10-0" + string(rune('0'+d)))
	}
	if len(f.alerts) != 1 {
		t.Fatalf("再破未满 7 天不应告警: %v", f.alerts)
	}
	run("2026-10-07")
	if len(f.alerts) != 2 {
		t.Fatalf("新一轮第 7 天应再次告警: %v", f.alerts)
	}
}
