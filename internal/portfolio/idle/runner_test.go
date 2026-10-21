package idle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
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

func idlePool() []pgstore.ExplorerIdlePool {
	// 10-21 判据修：眠钱=Idle（余额>0 且 30d 零收入）——FetchedAt 须鲜（鲜度闸）。
	return []pgstore.ExplorerIdlePool{{Venue: "binance", VenueType: "spot", Balance: 19300, Currency: "USD",
		Income30dUSD: 0, Idle: true, FetchedAt: time.Now()}}
}

// D-534 锚（10-21 判据修后 Idle 口径）：连续 7 天眠→1 次告警+1 提案行；第 8 天
// 不重复；同日重跑幂等；有收益归零再眠重新计。
func TestSleepmonStreak(t *testing.T) {
	dir := t.TempDir()
	f := &fakeStore{pools: idlePool(), streaks: map[string]pgstore.IdleStreak{}}
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
	// 回上（30d 内有收益）→归零复位
	f.pools[0].Idle = false
	f.pools[0].Income30dUSD = 4.23
	run("2026-09-29")
	if st := f.streaks["binance\x00spot"]; st.Days != 0 || st.Alerted {
		t.Fatalf("回上应归零复位: %+v", st)
	}
	// 再眠→重新计，第 7 天才告警
	f.pools[0].Idle = true
	f.pools[0].Income30dUSD = 0
	for d := 1; d <= 6; d++ {
		run("2026-10-0" + string(rune('0'+d)))
	}
	if len(f.alerts) != 1 {
		t.Fatalf("再眠未满 7 天不应告警: %v", f.alerts)
	}
	run("2026-10-07")
	if len(f.alerts) != 2 {
		t.Fatalf("新一轮第 7 天应再次告警: %v", f.alerts)
	}
}

// TestSleepmonStaleSnapSkip 鲜度闸锚（10-21 判据修）：快照行龄>36h 的池=断供面
// 不判不写——对抗锚：删 idleSnapFreshMax 门此测必红（陈行池会被计入眠钱簿）。
func TestSleepmonStaleSnapSkip(t *testing.T) {
	dir := t.TempDir()
	f := &fakeStore{pools: []pgstore.ExplorerIdlePool{{
		Venue: "15032313", VenueType: "mt5", Balance: 1000, Currency: "USD",
		Idle: true, FetchedAt: time.Now().Add(-72 * time.Hour), // 陈行（10-02 形实拍）
	}}, streaks: map[string]pgstore.IdleStreak{}}
	deps := Deps{Store: f, InboxPath: filepath.Join(dir, "inbox.md")}
	if err := checkDay(context.Background(), deps, "2026-10-21", bjLoc); err != nil {
		t.Fatal(err)
	}
	if len(f.streaks) != 0 || len(f.alerts) != 0 {
		t.Fatalf("陈行池应整池跳过（不判不写）: streaks=%v alerts=%v", f.streaks, f.alerts)
	}
}
